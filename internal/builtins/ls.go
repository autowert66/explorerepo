package builtins

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"explorerepo/internal/object"
	"explorerepo/internal/vfs"
)

func init() {
	register("ls", "list directory contents", lsCmd)
	register("tree", "list directories as a tree", treeCmd)
}

type lsItem struct {
	name string
	node *vfs.Node
}

func lsCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("ls", args, []Opt{{Short: 'a'}, {Short: 'l'}, {Short: '1'}, {Short: 'R'}, {Short: 'd'}, {Short: 'F'}, {Short: 'A'}}, "")
	if !ok {
		return ExitUsage
	}
	all, long, rec, dirOnly, classify := p.Has("a"), p.Has("l"), p.Has("R"), p.Has("d"), p.Has("F")
	almost := p.Has("A")
	paths := p.Args
	if len(paths) == 0 {
		paths = []string{"."}
	}
	status := ExitOK
	var files []lsItem
	type dirArg struct {
		disp string
		node *vfs.Node
	}
	var dirs []dirArg
	for _, arg := range paths {
		abs := env.Abs(arg)
		n, err := env.FS.Lstat(ctx, abs)
		if err == nil && n.IsSymlink() && !dirOnly && !long && !classify {
			n, err = env.FS.Stat(ctx, abs)
		}
		if err != nil {
			env.PathErr("ls", arg, err)
			status = ExitFail
			continue
		}
		if (n.IsDir() || n.IsGitlink()) && !dirOnly {
			dirs = append(dirs, dirArg{arg, n})
		} else {
			files = append(files, lsItem{arg, n})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].name < files[j].name })
	headers := len(paths) > 1 || rec
	first := true
	if len(files) > 0 {
		lsPrint(ctx, env, files, long, classify)
		first = false
	}
	var listDir func(disp string, n *vfs.Node) bool
	listDir = func(disp string, n *vfs.Node) bool {
		if ctx.Err() != nil || env.Stdout.Err() != nil {
			return false
		}
		kids, err := env.FS.Children(ctx, n)
		if err != nil {
			env.PathErr("ls", disp, err)
			status = ExitFail
			return true
		}
		if !first {
			env.Stdout.WriteString("\n")
		}
		first = false
		if headers {
			env.Stdout.Printf("%s:\n", disp)
		}
		var items []lsItem
		if all {
			items = append(items, lsItem{".", n}, lsItem{"..", n})
		}
		for _, k := range kids {
			if strings.HasPrefix(k.Name, ".") && !all && !almost {
				continue
			}
			items = append(items, lsItem{k.Name, k})
		}
		lsPrint(ctx, env, items, long, classify)
		if rec {
			for _, k := range kids {
				if k.IsDir() && !(strings.HasPrefix(k.Name, ".") && !all && !almost) {
					if !listDir(joinDisplay(disp, k.Name), k) {
						return false
					}
				}
			}
		}
		return true
	}
	for _, d := range dirs {
		if rec {
			if err := env.Prefetch(ctx, d.node, -1); err != nil {
				env.PathErr("ls", d.disp, err)
				status = ExitFail
				continue
			}
		}
		if !listDir(d.disp, d.node) {
			return ExitSigPipe
		}
	}
	return status
}

func lsPrint(ctx context.Context, env *Env, items []lsItem, long, classify bool) {
	if !long {
		for _, it := range items {
			env.Stdout.WriteString(it.name + classifySuffix(it.node, classify) + "\n")
		}
		return
	}
	// one batched lookup for sizes and one for symlink targets
	var sizeOIDs, linkOIDs []object.OID
	for _, it := range items {
		switch {
		case it.node.IsRegular():
			sizeOIDs = append(sizeOIDs, it.node.OID)
		case it.node.IsSymlink():
			linkOIDs = append(linkOIDs, it.node.OID)
		}
	}
	sizes := env.Repo.Sizes(ctx, sizeOIDs)
	env.Repo.Blobs(ctx, linkOIDs)
	cols := make([]string, len(items))
	width := 1
	for i, it := range items {
		switch {
		case it.node.IsRegular():
			if v, ok := sizes[it.node.OID]; ok {
				cols[i] = strconv.FormatInt(v, 10)
			} else {
				cols[i] = "?"
			}
		case it.node.IsSymlink():
			if b, err := env.Repo.Blob(ctx, it.node.OID); err == nil {
				cols[i] = strconv.Itoa(len(b))
			} else {
				cols[i] = "?"
			}
		default:
			cols[i] = "-"
		}
		if len(cols[i]) > width {
			width = len(cols[i])
		}
	}
	for i, it := range items {
		name := it.name + classifySuffix(it.node, classify)
		if it.node.IsSymlink() {
			if b, err := env.Repo.Blob(ctx, it.node.OID); err == nil {
				name += " -> " + string(b)
			}
		}
		env.Stdout.Printf("%s %*s %s\n", modeString(it.node), width, cols[i], name)
	}
}

func classifySuffix(n *vfs.Node, on bool) string {
	if !on {
		return ""
	}
	switch {
	case n.IsDir() || n.IsGitlink():
		return "/"
	case n.IsSymlink():
		return "@"
	case n.IsExec():
		return "*"
	}
	return ""
}

func treeCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("tree", args, []Opt{{Short: 'L', Arg: true}, {Short: 'd'}, {Short: 'a'}, {Short: 'F'}}, "")
	if !ok {
		return ExitUsage
	}
	maxDepth := -1
	if v := p.Val("L"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return env.usage("tree", "Invalid level, must be greater than 0.")
		}
		maxDepth = n
	}
	dirsOnly, all, classify := p.Has("d"), p.Has("a"), p.Has("F")
	arg := "."
	if len(p.Args) > 0 {
		arg = p.Args[0]
	}
	root, err := env.FS.Stat(ctx, env.Abs(arg))
	if err != nil {
		env.PathErr("tree", arg, err)
		return ExitFail
	}
	env.Stdout.WriteString(arg + "\n")
	if !root.IsDir() {
		env.Stdout.WriteString("\n0 directories, 1 file\n")
		return ExitOK
	}
	if err := env.Prefetch(ctx, root, maxDepth); err != nil {
		env.PathErr("tree", arg, err)
		return ExitFail
	}
	type line struct {
		prefix string
		n      *vfs.Node
	}
	var lines []line
	var links []object.OID
	dirsN, filesN := 0, 0
	var rec func(n *vfs.Node, prefix string, depth int) error
	rec = func(n *vfs.Node, prefix string, depth int) error {
		kids, err := env.FS.Children(ctx, n)
		if err != nil {
			return err
		}
		var vis []*vfs.Node
		for _, k := range kids {
			if strings.HasPrefix(k.Name, ".") && !all {
				continue
			}
			if dirsOnly && !(k.IsDir() || k.IsGitlink()) {
				continue
			}
			vis = append(vis, k)
		}
		for i, k := range vis {
			last := i == len(vis)-1
			conn, next := "├── ", "│   "
			if last {
				conn, next = "└── ", "    "
			}
			lines = append(lines, line{prefix + conn, k})
			if k.IsSymlink() {
				links = append(links, k.OID)
			}
			if k.IsDir() {
				dirsN++
				if maxDepth < 0 || depth < maxDepth {
					if err := rec(k, prefix+next, depth+1); err != nil {
						return err
					}
				}
			} else if k.IsGitlink() {
				dirsN++
			} else {
				filesN++
			}
		}
		return nil
	}
	if err := rec(root, "", 1); err != nil {
		env.PathErr("tree", arg, err)
		return ExitFail
	}
	env.Repo.Blobs(ctx, links)
	for _, l := range lines {
		name := l.n.Name + classifySuffix(l.n, classify)
		if l.n.IsSymlink() {
			if b, err := env.Repo.Blob(ctx, l.n.OID); err == nil {
				name += " -> " + string(b)
			}
		}
		env.Stdout.WriteString(l.prefix + name + "\n")
	}
	summary := fmt.Sprintf("%d %s", dirsN, plural(dirsN, "directory", "directories"))
	if !dirsOnly {
		summary += fmt.Sprintf(", %d %s", filesN, plural(filesN, "file", "files"))
	}
	env.Stdout.WriteString("\n" + summary + "\n")
	return ExitOK
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
