package builtins

import (
	"context"
	"fmt"
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
		env.Stdout.WriteString(Help(args))
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
	p, ok := env.parse("log", args, []Opt{{Short: 'n', Arg: true}, {Long: "oneline"}, {Long: "max-count", Arg: true, Key: "n"}}, "n")
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
	for i, e := range entries {
		msg := strings.TrimRight(e.Commit.Message, "\n")
		if p.Has("oneline") {
			subj, _, _ := strings.Cut(msg, "\n")
			env.Stdout.Printf("%s %s\n", e.OID.String()[:7], subj)
			continue
		}
		if i > 0 {
			env.Stdout.WriteString("\n")
		}
		env.Stdout.Printf("commit %s\nAuthor: %s\nDate:   %s\n\n", e.OID, authorName(e.Commit.Author), e.Commit.AuthorTime.Format("Mon Jan _2 15:04:05 2006 -0700"))
		for _, l := range strings.Split(msg, "\n") {
			env.Stdout.Printf("    %s\n", l)
		}
	}
	return ExitOK
}

const gitUsage = "only these git subcommands are available: log [-n N] [--oneline], show REV:PATH, ls-tree [-r] [-d] [--name-only] REV [PATH], rev-parse [REV]"

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
