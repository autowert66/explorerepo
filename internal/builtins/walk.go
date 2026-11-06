package builtins

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/vfs"
)

// ErrSkipDir may be returned from a Walk callback to avoid descending.
var ErrSkipDir = errors.New("skip dir")

type WalkEntry struct {
	Display string
	Node    *vfs.Node
	Depth   int
}

// lazyDepthLimit: finite depths up to this are fetched level by level; deeper
// or unbounded walks prefetch the whole subtree with one blob:none request.
const lazyDepthLimit = 3

// Prefetch makes the trees needed to walk node to maxDepth (-1 = unlimited)
// available, using as few requests as possible.
func (e *Env) Prefetch(ctx context.Context, n *vfs.Node, maxDepth int) error {
	if !n.IsDir() || maxDepth == 0 {
		return nil
	}
	if maxDepth < 0 || maxDepth > lazyDepthLimit {
		return e.Repo.PrefetchSubtrees(ctx, n.OID)
	}
	level := []object.OID{n.OID}
	for d := 0; d < maxDepth && len(level) > 0; d++ {
		if err := e.Repo.Trees(ctx, level); err != nil {
			return err
		}
		var next []object.OID
		for _, oid := range level {
			ents, err := e.Repo.Tree(ctx, oid)
			if err != nil {
				return err
			}
			for _, en := range ents {
				if en.IsDir() {
					next = append(next, en.OID)
				}
			}
		}
		level = next
	}
	return nil
}

type WalkOpts struct {
	MaxDepth int // -1 unlimited; the start node is depth 0
}

func joinDisplay(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// Walk visits start and everything below it in sorted pre-order. Symlinks are
// reported but never followed. Returning ErrSkipDir from fn skips a directory.
func (e *Env) Walk(ctx context.Context, start *vfs.Node, display string, o WalkOpts, fn func(WalkEntry) error) error {
	if err := e.Prefetch(ctx, start, o.MaxDepth); err != nil {
		return err
	}
	var visit func(n *vfs.Node, disp string, depth int) error
	visit = func(n *vfs.Node, disp string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.Stdout.Err(); err != nil {
			return err
		}
		if err := fn(WalkEntry{disp, n, depth}); err != nil {
			if err == ErrSkipDir {
				return nil
			}
			return err
		}
		if !n.IsDir() || (o.MaxDepth >= 0 && depth >= o.MaxDepth) {
			return nil
		}
		kids, err := e.FS.Children(ctx, n)
		if err != nil {
			return err
		}
		for _, k := range kids {
			if err := visit(k, joinDisplay(disp, k.Name), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(start, display, 0)
}

// ---- reading files in batches ----

type fileResult struct {
	Name string
	Node *vfs.Node
	Data []byte
	Err  error
}

// readFiles resolves all names first and fetches every missing blob with one
// request, then returns the contents. "-" reads stdin.
func (e *Env) readFiles(ctx context.Context, names []string) []fileResult {
	res := make([]fileResult, len(names))
	var oids []object.OID
	for i, name := range names {
		res[i].Name = name
		if name == "-" {
			continue
		}
		n, err := e.FS.Stat(ctx, e.Abs(name))
		if err != nil {
			res[i].Err = err
			continue
		}
		if n.IsDir() || n.IsGitlink() {
			if n.IsGitlink() {
				res[i].Err = &vfs.SubmoduleError{Commit: n.OID}
			} else {
				res[i].Err = vfs.ErrIsDir
			}
			continue
		}
		res[i].Node = n
		oids = append(oids, n.OID)
	}
	fetchErr := e.Repo.Blobs(ctx, oids)
	for i := range res {
		if res[i].Err != nil {
			continue
		}
		if res[i].Name == "-" {
			res[i].Data, res[i].Err = io.ReadAll(e.Stdin)
			continue
		}
		if fetchErr != nil {
			res[i].Err = fetchErr
			continue
		}
		res[i].Data, res[i].Err = e.FS.Read(ctx, res[i].Node)
	}
	return res
}

// splitLines splits data into lines without terminators; a trailing newline
// does not produce an empty last element.
func splitLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	var lines [][]byte
	for len(data) > 0 {
		i := indexByte(data, '\n')
		if i < 0 {
			lines = append(lines, data)
			break
		}
		lines = append(lines, data[:i])
		data = data[i+1:]
	}
	return lines
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
