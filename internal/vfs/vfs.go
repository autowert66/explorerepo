// Package vfs presents one commit of a repo as a read-only POSIX-ish tree.
package vfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/autowert66/git-explorer/internal/object"
	"github.com/autowert66/git-explorer/internal/repo"
)

// errno is an error with a coreutils-style message that still matches fs errors.
type errno struct {
	msg string
	is  error
}

func (e *errno) Error() string        { return e.msg }
func (e *errno) Is(target error) bool { return e.is != nil && target == e.is }

var (
	ErrNotExist    = &errno{"No such file or directory", fs.ErrNotExist}
	ErrIsDir       = &errno{"Is a directory", nil}
	ErrNotDir      = &errno{"Not a directory", nil}
	ErrLoop        = &errno{"Too many levels of symbolic links", nil}
	ErrOutside     = &errno{"symlink points outside repository", fs.ErrNotExist}
	ErrReadOnly    = &errno{"read-only file system", fs.ErrPermission}
	maxSymlinkHops = 40
)

// SubmoduleError is returned when touching a gitlink.
type SubmoduleError struct{ Commit object.OID }

func (e *SubmoduleError) Error() string {
	return fmt.Sprintf("submodule (commit %s), not fetched", e.Commit)
}

// Node is a resolved path inside the repository.
type Node struct {
	Path  string // cleaned absolute virtual path, symlinks in directories resolved
	Name  string
	Mode  uint32 // git mode; ModeDir for root
	OID   object.OID
	mtime time.Time
}

func (n *Node) IsDir() bool     { return n.Mode == object.ModeDir }
func (n *Node) IsSymlink() bool { return n.Mode&0o170000 == object.ModeSymlink }
func (n *Node) IsGitlink() bool { return n.Mode == object.ModeGitlink }
func (n *Node) IsRegular() bool { return n.Mode&0o170000 == 0o100000 }
func (n *Node) IsExec() bool    { return n.IsRegular() && n.Mode&0o111 != 0 }

type FS struct {
	repo  *repo.Repo
	root  *Node
	mtime time.Time
	Warn  func(msg string) // called once per distinct warning

	mu     sync.Mutex
	warned map[string]bool
}

func New(r *repo.Repo) *FS {
	info := r.Info()
	return NewAt(r, info.RootTree, info.Time)
}

// NewAt presents an arbitrary root tree.
func NewAt(r *repo.Repo, tree object.OID, mtime time.Time) *FS {
	root := &Node{Path: "/", Name: "", Mode: object.ModeDir, OID: tree, mtime: mtime}
	return &FS{repo: r, root: root, mtime: mtime, warned: map[string]bool{}}
}

func (f *FS) Repo() *repo.Repo { return f.repo }

func (f *FS) warn(msg string) {
	f.mu.Lock()
	seen := f.warned[msg]
	f.warned[msg] = true
	f.mu.Unlock()
	if !seen && f.Warn != nil {
		f.Warn(msg)
	}
}

func badName(n string) bool {
	return n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00")
}

// entries lists a directory node's visible entries sorted by name.
func (f *FS) entries(ctx context.Context, dir *Node) ([]object.Entry, error) {
	if dir.IsGitlink() {
		return nil, &SubmoduleError{dir.OID}
	}
	raw, err := f.repo.Tree(ctx, dir.OID)
	if err != nil {
		return nil, err
	}
	out := make([]object.Entry, 0, len(raw))
	for _, e := range raw {
		if badName(e.Name) {
			f.warn(fmt.Sprintf("ignoring tree entry with invalid name %q", e.Name))
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *FS) child(dir *Node, e object.Entry) *Node {
	p := path.Join(dir.Path, e.Name)
	return &Node{Path: p, Name: e.Name, Mode: e.Mode, OID: e.OID, mtime: f.mtime}
}

func split(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

// resolve walks p; followLast controls whether a trailing symlink is followed.
func (f *FS) resolve(ctx context.Context, p string, followLast bool) (*Node, error) {
	p = path.Clean("/" + p)
	comps := split(p)
	hops := 0
restart:
	cur := f.root
	for i := 0; i < len(comps); i++ {
		comp := comps[i]
		if comp == ".." { // only possible after symlink expansion
			if cur == f.root {
				return nil, ErrOutside
			}
			return nil, ErrNotExist
		}
		if cur.IsGitlink() {
			return nil, &SubmoduleError{cur.OID}
		}
		if !cur.IsDir() {
			return nil, ErrNotDir
		}
		ents, err := f.entries(ctx, cur)
		if err != nil {
			return nil, err
		}
		idx := sort.Search(len(ents), func(k int) bool { return ents[k].Name >= comp })
		if idx == len(ents) || ents[idx].Name != comp {
			return nil, ErrNotExist
		}
		n := f.child(cur, ents[idx])
		last := i == len(comps)-1
		if n.IsSymlink() && (!last || followLast) {
			hops++
			if hops > maxSymlinkHops {
				return nil, ErrLoop
			}
			target, err := f.repo.Blob(ctx, n.OID)
			if err != nil {
				return nil, err
			}
			ts := string(target)
			if strings.HasPrefix(ts, "/") {
				return nil, ErrOutside
			}
			prefix := comps[:i:i]
			for _, tc := range split(ts) {
				if tc == ".." {
					if len(prefix) == 0 {
						return nil, ErrOutside
					}
					prefix = prefix[:len(prefix)-1]
				} else {
					prefix = append(prefix[:len(prefix):len(prefix)], tc)
				}
			}
			comps = append(append([]string{}, prefix...), comps[i+1:]...)
			goto restart
		}
		cur = n
	}
	return cur, nil
}

// Lstat resolves p without following a trailing symlink.
func (f *FS) Lstat(ctx context.Context, p string) (*Node, error) { return f.resolve(ctx, p, false) }

// Stat resolves p following symlinks.
func (f *FS) Stat(ctx context.Context, p string) (*Node, error) { return f.resolve(ctx, p, true) }

// ReadDir lists a directory (following symlinks to directories).
func (f *FS) ReadDir(ctx context.Context, p string) ([]*Node, error) {
	n, err := f.Stat(ctx, p)
	if err != nil {
		return nil, err
	}
	return f.Children(ctx, n)
}

// Children lists the entries of an already-resolved directory node.
func (f *FS) Children(ctx context.Context, n *Node) ([]*Node, error) {
	if n.IsGitlink() {
		return nil, &SubmoduleError{n.OID}
	}
	if !n.IsDir() {
		return nil, ErrNotDir
	}
	ents, err := f.entries(ctx, n)
	if err != nil {
		return nil, err
	}
	out := make([]*Node, len(ents))
	for i, e := range ents {
		out[i] = f.child(n, e)
	}
	return out, nil
}

// ReadFile returns file contents, following symlinks.
func (f *FS) ReadFile(ctx context.Context, p string) ([]byte, error) {
	n, err := f.Stat(ctx, p)
	if err != nil {
		return nil, err
	}
	return f.Read(ctx, n)
}

// Read returns the contents of a resolved node.
func (f *FS) Read(ctx context.Context, n *Node) ([]byte, error) {
	switch {
	case n.IsGitlink():
		return nil, &SubmoduleError{n.OID}
	case n.IsDir():
		return nil, ErrIsDir
	case n.IsSymlink():
		return nil, ErrNotExist
	}
	return f.repo.Blob(ctx, n.OID)
}

// Readlink returns a symlink's target.
func (f *FS) Readlink(ctx context.Context, p string) (string, error) {
	n, err := f.Lstat(ctx, p)
	if err != nil {
		return "", err
	}
	if !n.IsSymlink() {
		return "", &errno{"Invalid argument", nil}
	}
	b, err := f.repo.Blob(ctx, n.OID)
	return string(b), err
}

// Size returns the blob size if known without network access.
func (f *FS) Size(ctx context.Context, n *Node) (int64, bool) {
	if !n.IsRegular() && !n.IsSymlink() {
		return 0, false
	}
	m := f.repo.Sizes(ctx, []object.OID{n.OID})
	v, ok := m[n.OID]
	return v, ok
}

// Info adapts a Node to fs.FileInfo. Size is 0 when unknown.
type Info struct {
	N         *Node
	SizeBytes int64
	SizeKnown bool
}

func (i Info) Name() string { return i.N.Name }
func (i Info) Size() int64  { return i.SizeBytes }
func (i Info) Mode() fs.FileMode {
	switch {
	case i.N.IsDir() || i.N.IsGitlink():
		return fs.ModeDir | 0o555
	case i.N.IsSymlink():
		return fs.ModeSymlink | 0o777
	case i.N.IsExec():
		return 0o555
	}
	return 0o444
}
func (i Info) ModTime() time.Time { return i.N.mtime }
func (i Info) IsDir() bool        { return i.N.IsDir() || i.N.IsGitlink() }
func (i Info) Sys() any           { return i.N }

// FileInfo builds an fs.FileInfo, using sizes already in cache.
func (f *FS) FileInfo(ctx context.Context, n *Node) Info {
	i := Info{N: n}
	if n.IsRegular() || n.IsSymlink() {
		if sz := f.repo.CachedSize(n.OID); sz >= 0 {
			i.SizeBytes, i.SizeKnown = sz, true
		}
	}
	return i
}

// IsNotExist reports whether err means a missing path.
func IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
