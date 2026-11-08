package builtins

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/vfs"
)

func init() {
	register("refs", "list branches and tags (refs [PREFIX])", refsCmd)
	register("log", "commit history of the current revision (no path filtering)", logCmd)
	register("git", "tiny git shim: log, show REV:PATH, ls-tree, rev-parse", gitCmd)
	register("help", "show this manual", func(ctx context.Context, env *Env, args []string) int {
		text, ok := Help(args)
		env.Stdout.WriteString(HelpStyled(env.Color, args, text, ok))
		if !ok {
			return ExitUsage
		}
		return ExitOK
	})
}

func refsCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("refs", args, nil, "")
	if !ok {
		return ExitUsage
	}
	prefixes := []string{"HEAD", "refs/heads/", "refs/tags/"}
	if len(p.Args) > 0 {
		prefixes = p.Args
	}
	refs, err := env.Repo.ListRefs(ctx, prefixes)
	if err != nil {
		env.PathErr("refs", "", err)
		return ExitFail
	}
	for _, r := range refs {
		if r.SymrefTarget != "" {
			env.Stdout.Printf("%s %s -> %s\n", r.OID, r.Name, r.SymrefTarget)
		} else {
			env.Stdout.Printf("%s %s\n", r.OID, r.Name)
		}
		if !r.Peeled.IsZero() {
			env.Stdout.Printf("%s %s^{}\n", r.Peeled, r.Name)
		}
	}
	return ExitOK
}

func authorName(sig string) string {
	if i := strings.LastIndex(sig, ">"); i >= 0 {
		return sig[:i+1]
	}
	return sig
}

func logCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("log", args, []Opt{
		{Short: 'n', Arg: true}, {Long: "oneline"}, {Long: "max-count", Arg: true, Key: "n"},
		{Long: "decorate"}, {Long: "no-decorate"},
	}, "n")
	if !ok {
		return ExitUsage
	}
	n := 10
	if v := p.Val("n"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1 {
			return env.usage("log", "invalid count %q", v)
		}
	}
	if len(p.Args) > 1 {
		return env.Unsupported("log", "path filtering", "log shows the history of the whole revision")
	}
	var start object.OID
	if len(p.Args) == 1 {
		oid, _, err := env.Repo.CommitTree(ctx, p.Args[0])
		if err != nil {
			env.PathErr("log", p.Args[0], err)
			return ExitFail
		}
		start = oid
	} else {
		start = env.Repo.Info().Commit
	}
	entries, err := env.Repo.Log(ctx, start, n)
	if err != nil {
		env.PathErr("log", "", err)
		return ExitFail
	}
	// Ref decorations follow git's log.decorate=auto: shown at a human
	// terminal, off for pipes and agents. --decorate / --no-decorate force it.
	decorate := env.Human
	if p.Has("decorate") {
		decorate = true
	}
	if p.Has("no-decorate") {
		decorate = false
	}
	var dec map[object.OID][]deco
	head := ""
	if decorate {
		dec, head = env.decorations(ctx)
	}
	for i, e := range entries {
		msg := strings.TrimRight(e.Commit.Message, "\n")
		refs := ""
		if decorate {
			refs = env.decorate(dec[e.OID], head)
		}
		if p.Has("oneline") {
			subj, _, _ := strings.Cut(msg, "\n")
			env.Stdout.WriteString(env.Color.Yellow(e.OID.String()[:7]))
			env.Stdout.WriteString(refs)
			env.Stdout.Printf(" %s\n", subj)
			continue
		}
		if i > 0 {
			env.Stdout.WriteString("\n")
		}
		env.Stdout.WriteString(env.Color.Yellow("commit " + e.OID.String()))
		env.Stdout.WriteString(refs)
		env.Stdout.Printf("\nAuthor: %s\nDate:   %s\n\n", authorName(e.Commit.Author), e.Commit.AuthorTime.Format("Mon Jan _2 15:04:05 2006 -0700"))
		for _, l := range strings.Split(msg, "\n") {
			env.Stdout.Printf("    %s\n", l)
		}
	}
	return ExitOK
}

// decoKind is the type of a ref shown by `log --decorate`.
type decoKind int

const (
	decoLocal decoKind = iota
	decoRemote
	decoTag
	decoHEAD
)

// deco is one ref shown after a commit hash, as in git log --decorate.
type deco struct {
	ref  string // full refname, or "HEAD"
	name string // the short name git prints
	kind decoKind
}

// decorations maps each commit to the refs that point at it, in the order git
// prints them: HEAD first, then refs by descending name. head is the branch
// HEAD resolves to, if any, so HEAD can be shown as "HEAD -> branch".
func (e *Env) decorations(ctx context.Context) (map[object.OID][]deco, string) {
	refs, err := e.Repo.ListRefs(ctx, []string{"HEAD", "refs/heads/", "refs/remotes/", "refs/tags/"})
	if err != nil {
		return nil, ""
	}
	m := map[object.OID][]deco{}
	head := ""
	for _, r := range refs {
		switch {
		case r.Name == "HEAD":
			if r.SymrefTarget != "" {
				head = r.SymrefTarget
			}
			m[r.OID] = append(m[r.OID], deco{"HEAD", "HEAD", decoHEAD})
		case strings.HasPrefix(r.Name, "refs/heads/"):
			m[r.OID] = append(m[r.OID], deco{r.Name, r.Name[len("refs/heads/"):], decoLocal})
		case strings.HasPrefix(r.Name, "refs/remotes/"):
			m[r.OID] = append(m[r.OID], deco{r.Name, r.Name[len("refs/remotes/"):], decoRemote})
		case strings.HasPrefix(r.Name, "refs/tags/"):
			oid := r.OID
			if !r.Peeled.IsZero() {
				oid = r.Peeled // an annotated tag decorates the commit it peels to
			}
			m[oid] = append(m[oid], deco{r.Name, r.Name[len("refs/tags/"):], decoTag})
		}
	}
	for _, ds := range m {
		sort.SliceStable(ds, func(i, j int) bool {
			if hi, hj := ds[i].kind == decoHEAD, ds[j].kind == decoHEAD; hi != hj {
				return hi // HEAD first
			}
			return ds[i].ref > ds[j].ref // then refs by descending name, like git
		})
	}
	return m, head
}

// decorate renders the " (HEAD -> main, origin/main, tag: v1)" suffix git shows
// after a commit hash, or "" when the commit has no refs.
func (e *Env) decorate(ds []deco, head string) string {
	if len(ds) == 0 {
		return ""
	}
	current := ""
	for _, d := range ds {
		if d.kind == decoLocal && d.ref == head {
			current = d.name
		}
	}
	var b strings.Builder
	b.WriteString(e.Color.Yellow(" ("))
	first := true
	for _, d := range ds {
		if d.kind == decoLocal && d.ref == head {
			continue // shown merged into HEAD below, as git does
		}
		if !first {
			b.WriteString(e.Color.Yellow(", "))
		}
		first = false
		switch d.kind {
		case decoHEAD:
			b.WriteString(e.Color.BoldCyan("HEAD"))
			if current != "" {
				b.WriteString(e.Color.Yellow(" -> "))
				b.WriteString(e.Color.BoldGreen(current))
			}
		case decoLocal:
			b.WriteString(e.Color.BoldGreen(d.name))
		case decoRemote:
			b.WriteString(e.Color.BoldRed(d.name))
		case decoTag:
			b.WriteString(e.Color.BoldYellow("tag: " + d.name))
		}
	}
	b.WriteString(e.Color.Yellow(")"))
	return b.String()
}

const gitUsage = "only these git subcommands are available: log [-n N] [--oneline] [--decorate], show REV:PATH, ls-tree [-r] [-d] [--name-only] REV [PATH], rev-parse [REV]"

func gitCmd(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		return env.usage("git", "%s", gitUsage)
	}
	switch args[0] {
	case "log":
		return logCmd(ctx, env, args[1:])
	case "rev-parse":
		rev := "HEAD"
		if len(args) > 1 {
			rev = args[1]
		}
		oid, _, err := env.Repo.CommitTree(ctx, rev)
		if err != nil {
			env.PathErr("git rev-parse", rev, err)
			return ExitFail
		}
		env.Stdout.Printf("%s\n", oid)
		return ExitOK
	case "show":
		if len(args) != 2 || !strings.Contains(args[1], ":") {
			return env.usage("git", "show: only 'git show REV:PATH' is supported")
		}
		rev, p, _ := strings.Cut(args[1], ":")
		fsys, err := env.fsAt(ctx, rev)
		if err != nil {
			env.PathErr("git show", rev, err)
			return ExitFail
		}
		n, err := fsys.Stat(ctx, "/"+p)
		if err != nil {
			env.PathErr("git show", args[1], err)
			return ExitFail
		}
		if n.IsDir() {
			return env.lsTree(ctx, fsys, n, "", p, false, false, true)
		}
		data, err := fsys.Read(ctx, n)
		if err != nil {
			env.PathErr("git show", args[1], err)
			return ExitFail
		}
		env.Stdout.Write(data)
		return ExitOK
	case "ls-tree":
		p, ok := env.parse("git ls-tree", args[1:], []Opt{{Short: 'r'}, {Short: 'd'}, {Short: 't'}, {Long: "name-only"}}, "")
		if !ok {
			return ExitUsage
		}
		if len(p.Args) < 1 || len(p.Args) > 2 {
			return env.usage("git", "usage: git ls-tree [-r] [-d] [--name-only] REV [PATH]")
		}
		fsys, err := env.fsAt(ctx, p.Args[0])
		if err != nil {
			env.PathErr("git ls-tree", p.Args[0], err)
			return ExitFail
		}
		sub := ""
		if len(p.Args) == 2 {
			sub = p.Args[1]
		}
		n, err := fsys.Stat(ctx, "/"+sub)
		if err != nil {
			env.PathErr("git ls-tree", sub, err)
			return ExitFail
		}
		if !n.IsDir() || (sub != "" && !strings.HasSuffix(sub, "/") && !p.Has("r")) {
			env.lsTreeLine(n, strings.Trim(sub, "/"), p.Has("name-only"))
			return ExitOK
		}
		return env.lsTree(ctx, fsys, n, strings.Trim(sub, "/"), sub, p.Has("r"), p.Has("d"), p.Has("name-only"))
	}
	return env.usage("git", "%s", gitUsage)
}

// fsAt returns the filesystem for rev (the current one when rev is the opened revision).
func (e *Env) fsAt(ctx context.Context, rev string) (*vfs.FS, error) {
	oid, ci, err := e.Repo.CommitTree(ctx, rev)
	if err != nil {
		return nil, err
	}
	if oid == e.Repo.Info().Commit {
		return e.FS, nil
	}
	return vfs.NewAt(e.Repo, ci.Tree, ci.Time), nil
}

func gitTypeName(n *vfs.Node) string {
	switch {
	case n.IsDir():
		return "tree"
	case n.IsGitlink():
		return "commit"
	}
	return "blob"
}

func (e *Env) lsTreeLine(n *vfs.Node, path string, nameOnly bool) {
	if nameOnly {
		e.Stdout.Printf("%s\n", path)
		return
	}
	e.Stdout.Printf("%06o %s %s\t%s\n", n.Mode, gitTypeName(n), n.OID, path)
}

// gitTreeKey is git's tree ordering: a directory sorts as if its name ended
// in "/", so "a.txt" comes before the directory "a".
func gitTreeKey(n *vfs.Node) string {
	if n.IsDir() {
		return n.Name + "/"
	}
	return n.Name
}

func sortGitTree(nodes []*vfs.Node) {
	sort.SliceStable(nodes, func(i, j int) bool { return gitTreeKey(nodes[i]) < gitTreeKey(nodes[j]) })
}

func (e *Env) lsTree(ctx context.Context, fsys *vfs.FS, root *vfs.Node, prefix, _ string, recursive, dirsOnly, nameOnly bool) int {
	if recursive {
		if err := e.Prefetch(ctx, root, -1); err != nil {
			e.PathErr("git ls-tree", prefix, err)
			return ExitFail
		}
	}
	var rec func(n *vfs.Node, pre string) error
	rec = func(n *vfs.Node, pre string) error {
		kids, err := fsys.Children(ctx, n)
		if err != nil {
			return err
		}
		sortGitTree(kids)
		for _, k := range kids {
			p := k.Name
			if pre != "" {
				p = pre + "/" + k.Name
			}
			if k.IsDir() && recursive {
				if dirsOnly {
					e.lsTreeLine(k, p, nameOnly)
				}
				if err := rec(k, p); err != nil {
					return err
				}
				continue
			}
			if dirsOnly && !k.IsDir() {
				continue
			}
			e.lsTreeLine(k, p, nameOnly)
			if e.Stdout.Err() != nil {
				return e.Stdout.Err()
			}
		}
		return nil
	}
	if err := rec(root, prefix); err != nil {
		if e.Stdout.Err() == nil {
			e.PathErr("git ls-tree", prefix, err)
		}
		return ExitFail
	}
	return ExitOK
}

var _ = fmt.Sprintf
var _ = time.Now
