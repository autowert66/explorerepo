package builtins

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/autowert66/git-explorer/internal/vfs"
)

func init() {
	register("cat", "concatenate files", catCmd)
	register("head", "first lines/bytes of files", headCmd)
	register("tail", "last lines/bytes of files", tailCmd)
	register("sed", "print line ranges (sed -n 'A,Bp')", sedCmd)
	register("wc", "count lines/words/bytes", wcCmd)
	register("nl", "number lines", nlCmd)
	register("stat", "describe a path", statCmd)
}

func catCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("cat", args, []Opt{{Short: 'n'}, {Short: 'b'}, {Short: 's'}, {Short: 'E'}}, "")
	if !ok {
		return ExitUsage
	}
	names := p.Args
	if len(names) == 0 {
		names = []string{"-"}
	}
	number, nonblank, squeeze, dollar := p.Has("n"), p.Has("b"), p.Has("s"), p.Has("E")
	plain := !number && !nonblank && !squeeze && !dollar
	status := ExitOK
	lineNo := 0
	prevBlank := false
	for _, r := range env.readFiles(ctx, names) {
		if ctx.Err() != nil || env.Stdout.Err() != nil {
			return ExitSigPipe
		}
		if r.Err != nil {
			env.PathErr("cat", r.Name, r.Err)
			status = ExitFail
			continue
		}
		if plain {
			env.Stdout.Write(r.Data)
			continue
		}
		for _, ln := range splitLines(r.Data) {
			blank := len(ln) == 0
			if squeeze && blank && prevBlank {
				continue
			}
			prevBlank = blank
			if number && !(nonblank && blank) || nonblank && !blank {
				lineNo++
				env.Stdout.Printf("%6d\t", lineNo)
			}
			env.Stdout.Write(ln)
			if dollar {
				env.Stdout.WriteString("$")
			}
			env.Stdout.WriteString("\n")
		}
	}
	return status
}

// countArg parses N for -n/-c: returns (n, fromStart "+N", negative "-N").
func countArg(s string) (n int, plus, neg bool, err error) {
	if strings.HasPrefix(s, "+") {
		plus, s = true, s[1:]
	} else if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	n, err = strconv.Atoi(s)
	if err != nil || n < 0 {
		err = fmt.Errorf("invalid number of lines: '%s'", s)
	}
	return
}

func multiHeader(env *Env, p *Parsed, count int, first *bool, name string) {
	if p.Has("q") || (count < 2 && !p.Has("v")) {
		return
	}
	if !*first {
		env.Stdout.WriteString("\n")
	}
	*first = false
	if name == "-" {
		name = "standard input"
	}
	env.Stdout.Printf("==> %s <==\n", name)
}

var headTailOpts = []Opt{{Short: 'n', Arg: true}, {Short: 'c', Arg: true}, {Short: 'q'}, {Short: 'v'}}

func headCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("head", args, headTailOpts, "n")
	if !ok {
		return ExitUsage
	}
	bytesMode := p.Has("c")
	cnt := "10"
	if bytesMode {
		cnt = p.Val("c")
	} else if p.Has("n") {
		cnt = p.Val("n")
	}
	n, _, neg, err := countArg(cnt)
	if err != nil {
		return env.usage("head", "%v", err)
	}
	names := p.Args
	if len(names) == 0 {
		names = []string{"-"}
	}
	first := true
	status := ExitOK
	// stdin streams: stop reading as soon as we have enough
	if len(names) == 1 && names[0] == "-" && !neg {
		return headStream(env, n, bytesMode)
	}
	for _, r := range env.readFiles(ctx, names) {
		if r.Err != nil {
			env.PathErr("head", r.Name, r.Err)
			status = ExitFail
			continue
		}
		multiHeader(env, p, len(names), &first, r.Name)
		data := r.Data
		switch {
		case bytesMode && neg:
			if n < len(data) {
				env.Stdout.Write(data[:len(data)-n])
			}
		case bytesMode:
			if n > len(data) {
				n = len(data)
			}
			env.Stdout.Write(data[:n])
		default:
			lines := splitLines(data)
			take := n
			if neg {
				take = len(lines) - n
			}
			for i := 0; i < take && i < len(lines); i++ {
				env.Stdout.Write(lines[i])
				env.Stdout.WriteString("\n")
			}
		}
		if env.Stdout.Err() != nil {
			return ExitSigPipe
		}
	}
	return status
}

func headStream(env *Env, n int, bytesMode bool) int {
	br := bufio.NewReader(env.Stdin)
	if bytesMode {
		io.CopyN(env.Stdout, br, int64(n))
		return ExitOK
	}
	for i := 0; i < n; i++ {
		line, err := br.ReadBytes('\n')
		env.Stdout.Write(line)
		if err != nil || env.Stdout.Err() != nil {
			break
		}
	}
	return ExitOK
}

func tailCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("tail", args, headTailOpts, "n")
	if !ok {
		return ExitUsage
	}
	bytesMode := p.Has("c")
	cnt := "10"
	if bytesMode {
		cnt = p.Val("c")
	} else if p.Has("n") {
		cnt = p.Val("n")
	}
	n, plus, _, err := countArg(cnt)
	if err != nil {
		return env.usage("tail", "%v", err)
	}
	names := p.Args
	if len(names) == 0 {
		names = []string{"-"}
	}
	first := true
	status := ExitOK
	for _, r := range env.readFiles(ctx, names) {
		if r.Err != nil {
			env.PathErr("tail", r.Name, r.Err)
			status = ExitFail
			continue
		}
		multiHeader(env, p, len(names), &first, r.Name)
		data := r.Data
		if bytesMode {
			switch {
			case plus && n > 0 && n-1 < len(data):
				env.Stdout.Write(data[n-1:])
			case plus && n > 0:
			case n < len(data):
				env.Stdout.Write(data[len(data)-n:])
			default:
				env.Stdout.Write(data)
			}
			continue
		}
		lines := splitLines(data)
		start := len(lines) - n
		if plus {
			start = n - 1
		}
		if start < 0 {
			start = 0
		}
		for i := start; i < len(lines); i++ {
			env.Stdout.Write(lines[i])
			env.Stdout.WriteString("\n")
		}
		if env.Stdout.Err() != nil {
			return ExitSigPipe
		}
	}
	return status
}

var sedPrint = regexp.MustCompile(`^(\$|\d+)(?:,(\$|\d+))?p$`)
var sedQuit = regexp.MustCompile(`^(\d+)q$`)

func sedCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("sed", args, []Opt{{Short: 'n'}, {Short: 'e', Arg: true}}, "")
	if !ok {
		return ExitUsage
	}
	var script string
	files := p.Args
	if es := p.Vals("e"); len(es) == 1 {
		script = es[0]
	} else if len(es) == 0 && len(files) > 0 {
		script, files = files[0], files[1:]
	} else {
		return env.Unsupported("sed", "multiple scripts", "use sed -n 'A,Bp', head or tail")
	}
	lo, hi, quitAt := 0, 0, 0
	if m := sedPrint.FindStringSubmatch(script); m != nil && p.Has("n") {
		toNum := func(s string) int {
			if s == "$" {
				return -1
			}
			v, _ := strconv.Atoi(s)
			return v
		}
		lo = toNum(m[1])
		hi = lo
		if m[2] != "" {
			hi = toNum(m[2])
		}
	} else if m := sedQuit.FindStringSubmatch(script); m != nil && !p.Has("n") {
		quitAt, _ = strconv.Atoi(m[1])
	} else {
		return env.Unsupported("sed", fmt.Sprintf("script %q", script), "only sed -n 'A,Bp', 'Ap', 'A,$p' and 'Nq'; use head, tail or grep for the rest")
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	status := ExitOK
	for _, r := range env.readFiles(ctx, files) {
		if r.Err != nil {
			env.PathErr("sed", r.Name, r.Err)
			status = ExitFail
			continue
		}
		lines := splitLines(r.Data)
		for i, ln := range lines {
			no := i + 1
			if quitAt > 0 {
				if no > quitAt {
					break
				}
			} else {
				from, to := lo, hi
				if from == -1 {
					from = len(lines)
				}
				if to == -1 {
					to = len(lines)
				}
				if no < from || no > to {
					continue
				}
			}
			env.Stdout.Write(ln)
			env.Stdout.WriteString("\n")
		}
		if env.Stdout.Err() != nil {
			return ExitSigPipe
		}
	}
	return status
}

func wcCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("wc", args, []Opt{{Short: 'l'}, {Short: 'w'}, {Short: 'c'}, {Short: 'm'}}, "")
	if !ok {
		return ExitUsage
	}
	l, w, c, m := p.Has("l"), p.Has("w"), p.Has("c"), p.Has("m")
	if !l && !w && !c && !m {
		l, w, c = true, true, true
	}
	names := p.Args
	if len(names) == 0 {
		names = []string{"-"}
	}
	type row struct {
		name       string
		l, w, c, m int
	}
	var rows []row
	var total row
	status := ExitOK
	for _, r := range env.readFiles(ctx, names) {
		if r.Err != nil {
			env.PathErr("wc", r.Name, r.Err)
			status = ExitFail
			continue
		}
		rw := row{name: r.Name, l: bytes.Count(r.Data, []byte{'\n'}), w: len(bytes.Fields(r.Data)), c: len(r.Data), m: utf8.RuneCount(r.Data)}
		if r.Name == "-" {
			rw.name = ""
		}
		rows = append(rows, rw)
		total.l += rw.l
		total.w += rw.w
		total.c += rw.c
		total.m += rw.m
	}
	if len(rows) > 1 {
		total.name = "total"
		rows = append(rows, total)
	}
	nfields := 0
	for _, b := range []bool{l, w, c, m} {
		if b {
			nfields++
		}
	}
	width := 1
	if nfields > 1 || len(rows) > 1 {
		width = len(strconv.Itoa(total.c))
		if len(names) == 1 && names[0] == "-" {
			width = 7
		}
	}
	for _, rw := range rows {
		var parts []string
		add := func(on bool, v int) {
			if on {
				parts = append(parts, fmt.Sprintf("%*d", width, v))
			}
		}
		add(l, rw.l)
		add(w, rw.w)
		add(m, rw.m)
		add(c, rw.c)
		line := strings.Join(parts, " ")
		if rw.name != "" {
			line += " " + rw.name
		}
		env.Stdout.WriteString(line + "\n")
	}
	return status
}

func nlCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("nl", args, []Opt{{Short: 'b', Arg: true}}, "")
	if !ok {
		return ExitUsage
	}
	all := p.Val("b") == "a"
	if b := p.Val("b"); b != "" && b != "a" && b != "t" {
		return env.Unsupported("nl", "-b "+b, "supported: -b a, -b t")
	}
	names := p.Args
	if len(names) == 0 {
		names = []string{"-"}
	}
	status := ExitOK
	no := 0
	for _, r := range env.readFiles(ctx, names) {
		if r.Err != nil {
			env.PathErr("nl", r.Name, r.Err)
			status = ExitFail
			continue
		}
		for _, ln := range splitLines(r.Data) {
			if len(ln) == 0 && !all {
				env.Stdout.WriteString("\n")
				continue
			}
			no++
			env.Stdout.Printf("%6d\t%s\n", no, ln)
		}
	}
	return status
}

func modeString(n *vfs.Node) string {
	switch {
	case n.IsDir() || n.IsGitlink():
		return "drwxr-xr-x"
	case n.IsSymlink():
		return "lrwxrwxrwx"
	case n.IsExec():
		return "-rwxr-xr-x"
	}
	return "-rw-r--r--"
}

func statCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("stat", args, nil, "")
	if !ok {
		return ExitUsage
	}
	if len(p.Args) == 0 {
		return env.usage("stat", "missing operand")
	}
	status := ExitOK
	for _, name := range p.Args {
		n, err := env.FS.Lstat(ctx, env.Abs(name))
		if err != nil {
			env.PathErr("stat", name, err)
			status = ExitFail
			continue
		}
		kind, oidLabel := "regular file", "Blob"
		switch {
		case n.IsDir():
			kind, oidLabel = "directory", "Tree"
		case n.IsSymlink():
			kind = "symbolic link"
		case n.IsGitlink():
			kind, oidLabel = "submodule", "Commit"
		case len(name) > 0 && n.Mode == 0o100755:
			kind = "regular file (executable)"
		}
		size := "-"
		if n.IsSymlink() {
			if t, err := env.FS.Readlink(ctx, env.Abs(name)); err == nil {
				size = strconv.Itoa(len(t))
			}
		} else if n.IsRegular() {
			size = "unknown"
			if v, ok := env.FS.Size(ctx, n); ok {
				size = strconv.FormatInt(v, 10)
			}
		}
		env.Stdout.Printf("  File: %s\n  Type: %s\n  Mode: %06o (%s)\n  Size: %s\n  %s: %s\n", name, kind, n.Mode, modeString(n), size, oidLabel, n.OID)
		if n.IsSymlink() {
			if t, err := env.FS.Readlink(ctx, env.Abs(name)); err == nil {
				env.Stdout.Printf("  Link: %s\n", t)
			}
		}
	}
	return status
}
