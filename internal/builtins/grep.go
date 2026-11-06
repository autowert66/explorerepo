package builtins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"explorerepo/internal/object"
	"explorerepo/internal/vfs"
)

func init() {
	register("grep", "search file contents (-rnivwlcoFE -A/-B/-C --include ...)", grepCmd)
	register("rg", "ripgrep-style recursive search (-i -l -c -w -F -g --files)", rgCmd)
}

type patMode int

const (
	modeBRE patMode = iota
	modeERE
	modeFixed
)

type grepCfg struct {
	name                      string
	patterns                  []string
	mode                      patMode
	ignoreCase, smartCase     bool
	invert, word, lineRegexp  bool
	count, listMatch, listNon bool
	onlyMatching, quiet       bool
	lineNum                   bool
	noFilename, withFilename  bool
	skipBinary, text          bool
	maxCount                  int
	after, before             int
	recursive                 bool
	include, exclude          []string
	excludeDir                []string
	hidden                    bool
	globs                     []string
	rg                        bool
	filesOnly                 bool
	noMessages                bool
}

// breToERE rewrites a POSIX basic regex into RE2 (ERE) syntax.
func breToERE(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case '\\':
			if i+1 >= len(rs) {
				b.WriteString(`\\`)
				continue
			}
			i++
			switch n := rs[i]; n {
			case '(', ')', '{', '}', '|', '+', '?':
				b.WriteRune(n)
			case '<', '>':
				b.WriteString(`\b`)
			default:
				b.WriteRune('\\')
				b.WriteRune(n)
			}
		case '(', ')', '{', '}', '|', '+', '?':
			b.WriteRune('\\')
			b.WriteRune(c)
		case '[':
			j := i + 1
			if j < len(rs) && rs[j] == '^' {
				j++
			}
			if j < len(rs) && rs[j] == ']' {
				j++
			}
			for j < len(rs) && rs[j] != ']' {
				if rs[j] == '[' && j+1 < len(rs) && (rs[j+1] == ':' || rs[j+1] == '.' || rs[j+1] == '=') {
					d := rs[j+1]
					k := j + 2
					for k+1 < len(rs) && !(rs[k] == d && rs[k+1] == ']') {
						k++
					}
					j = k + 1
				}
				j++
			}
			if j >= len(rs) {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(string(rs[i : j+1]))
			i = j
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (cfg *grepCfg) compile() (*regexp.Regexp, error) {
	var parts []string
	for _, p := range cfg.patterns {
		switch cfg.mode {
		case modeFixed:
			p = regexp.QuoteMeta(p)
		case modeBRE:
			p = breToERE(p)
		}
		parts = append(parts, "(?:"+p+")")
	}
	expr := strings.Join(parts, "|")
	if cfg.word {
		expr = `\b(?:` + expr + `)\b`
	}
	if cfg.lineRegexp {
		expr = `^(?:` + expr + `)$`
	}
	fold := cfg.ignoreCase
	if cfg.smartCase && !fold {
		fold = true
		for _, p := range cfg.patterns {
			if p != strings.ToLower(p) {
				fold = false
			}
		}
	}
	if fold {
		expr = "(?i)" + expr
	}
	return regexp.Compile("(?m)" + expr)
}

func grepCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("grep", args, []Opt{
		{Short: 'r'}, {Short: 'R'}, {Short: 'n'}, {Short: 'i'}, {Short: 'v'}, {Short: 'w'}, {Short: 'x'},
		{Short: 'l'}, {Short: 'L'}, {Short: 'c'}, {Short: 'o'}, {Short: 'h'}, {Short: 'H'},
		{Short: 'F'}, {Short: 'E'}, {Short: 'G'}, {Short: 'P'}, {Short: 'I'}, {Short: 'a'}, {Short: 'q'}, {Short: 's'},
		{Short: 'm', Arg: true}, {Short: 'A', Arg: true}, {Short: 'B', Arg: true}, {Short: 'C', Arg: true},
		{Short: 'e', Arg: true}, {Long: "include", Arg: true}, {Long: "exclude", Arg: true},
		{Long: "exclude-dir", Arg: true}, {Long: "color", Arg: true, Key: "color"}, {Long: "line-number", Key: "n"},
		{Long: "ignore-case", Key: "i"}, {Long: "recursive", Key: "r"}, {Long: "files-with-matches", Key: "l"},
		{Long: "invert-match", Key: "v"}, {Long: "count", Key: "c"}, {Long: "word-regexp", Key: "w"},
		{Long: "fixed-strings", Key: "F"}, {Long: "extended-regexp", Key: "E"}, {Long: "only-matching", Key: "o"},
		{Long: "no-filename", Key: "h"}, {Long: "with-filename", Key: "H"}, {Long: "quiet", Key: "q"},
		{Long: "text", Key: "a"}, {Long: "max-count", Arg: true, Key: "m"},
	}, "")
	if !ok {
		return ExitUsage
	}
	cfg := &grepCfg{name: "grep"}
	cfg.recursive = p.Has("r") || p.Has("R")
	cfg.lineNum = p.Has("n")
	cfg.ignoreCase, cfg.invert, cfg.word, cfg.lineRegexp = p.Has("i"), p.Has("v"), p.Has("w"), p.Has("x")
	cfg.listMatch, cfg.listNon, cfg.count = p.Has("l"), p.Has("L"), p.Has("c")
	cfg.onlyMatching, cfg.quiet = p.Has("o"), p.Has("q")
	cfg.noFilename, cfg.withFilename = p.Has("h"), p.Has("H")
	cfg.skipBinary, cfg.text, cfg.noMessages = p.Has("I"), p.Has("a"), p.Has("s")
	cfg.include, cfg.exclude, cfg.excludeDir = p.Vals("include"), p.Vals("exclude"), p.Vals("exclude-dir")
	switch {
	case p.Has("F"):
		cfg.mode = modeFixed
	case p.Has("E") || p.Has("P"):
		cfg.mode = modeERE
	}
	num := func(key string, dst *int) bool {
		if !p.Has(key) {
			return true
		}
		n, err := strconv.Atoi(p.Val(key))
		if err != nil || n < 0 {
			env.Errf("grep", "invalid %s argument", key)
			return false
		}
		*dst = n
		return true
	}
	var cc int
	if !num("m", &cfg.maxCount) || !num("A", &cfg.after) || !num("B", &cfg.before) || !num("C", &cc) {
		return ExitUsage
	}
	if p.Has("C") {
		cfg.after, cfg.before = cc, cc
	}
	rest := p.Args
	if es := p.Vals("e"); len(es) > 0 {
		for _, e := range es {
			cfg.patterns = append(cfg.patterns, strings.Split(e, "\n")...)
		}
	} else {
		if len(rest) == 0 {
			return env.usage("grep", "missing pattern (usage: grep [OPTIONS] PATTERN [FILE...])")
		}
		cfg.patterns = strings.Split(rest[0], "\n")
		rest = rest[1:]
	}
	return runGrep(ctx, env, cfg, rest)
}

func rgCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("rg", args, []Opt{
		{Short: 'i'}, {Short: 's'}, {Short: 'S'}, {Short: 'l'}, {Short: 'c'}, {Short: 'w'}, {Short: 'F'}, {Short: 'v'},
		{Short: 'n'}, {Short: 'N'}, {Short: 'H'}, {Short: 'I'}, {Short: 'q'}, {Short: 'o'}, {Short: 'u'}, {Short: 'a'},
		{Short: 'g', Arg: true}, {Short: 'e', Arg: true}, {Short: 'm', Arg: true},
		{Short: 'A', Arg: true}, {Short: 'B', Arg: true}, {Short: 'C', Arg: true}, {Short: 'j', Arg: true},
		{Long: "hidden"}, {Long: "files"}, {Long: "no-heading"}, {Long: "heading"}, {Long: "color", Arg: true},
		{Long: "files-with-matches", Key: "l"}, {Long: "count", Key: "c"}, {Long: "ignore-case", Key: "i"},
		{Long: "fixed-strings", Key: "F"}, {Long: "word-regexp", Key: "w"}, {Long: "glob", Arg: true, Key: "g"},
		{Long: "line-number", Key: "n"}, {Long: "no-line-number", Key: "N"}, {Long: "with-filename", Key: "H"},
		{Long: "no-filename", Key: "I"}, {Long: "max-count", Arg: true, Key: "m"}, {Long: "invert-match", Key: "v"},
		{Long: "smart-case", Key: "S"}, {Long: "case-sensitive", Key: "s"}, {Long: "regexp", Arg: true, Key: "e"},
		{Long: "only-matching", Key: "o"}, {Long: "quiet", Key: "q"}, {Long: "text", Key: "a"},
	}, "")
	if !ok {
		return ExitUsage
	}
	cfg := &grepCfg{name: "rg", rg: true, recursive: true, lineNum: !p.Has("N"), mode: modeERE}
	cfg.ignoreCase, cfg.smartCase = p.Has("i"), p.Has("S")
	cfg.word, cfg.invert, cfg.quiet, cfg.onlyMatching = p.Has("w"), p.Has("v"), p.Has("q"), p.Has("o")
	cfg.listMatch, cfg.count, cfg.hidden, cfg.filesOnly = p.Has("l"), p.Has("c"), p.Has("hidden"), p.Has("files")
	cfg.noFilename, cfg.withFilename, cfg.text = p.Has("I"), p.Has("H"), p.Has("a")
	cfg.globs = p.Vals("g")
	cfg.skipBinary = !cfg.text
	if p.Has("F") {
		cfg.mode = modeFixed
	}
	for _, kv := range []struct {
		key string
		dst *int
	}{{"m", &cfg.maxCount}, {"A", &cfg.after}, {"B", &cfg.before}} {
		if p.Has(kv.key) {
			n, err := strconv.Atoi(p.Val(kv.key))
			if err != nil || n < 0 {
				return env.usage("rg", "invalid number for -%s", kv.key)
			}
			*kv.dst = n
		}
	}
	if p.Has("C") {
		n, err := strconv.Atoi(p.Val("C"))
		if err != nil || n < 0 {
			return env.usage("rg", "invalid number for -C")
		}
		cfg.after, cfg.before = n, n
	}
	rest := p.Args
	if cfg.filesOnly {
		return runGrep(ctx, env, cfg, rest)
	}
	if es := p.Vals("e"); len(es) > 0 {
		cfg.patterns = es
	} else {
		if len(rest) == 0 {
			return env.usage("rg", "missing pattern (usage: rg [OPTIONS] PATTERN [PATH...])")
		}
		cfg.patterns = []string{rest[0]}
		rest = rest[1:]
	}
	return runGrep(ctx, env, cfg, rest)
}

type target struct {
	display string
	node    *vfs.Node // nil = stdin
}

func (cfg *grepCfg) globOK(rel string, isDir bool) bool {
	if len(cfg.globs) == 0 {
		return true
	}
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	match := func(pat string) bool {
		pat = strings.TrimPrefix(pat, "/")
		if strings.Contains(pat, "/") {
			return GlobMatch(pat, rel, false)
		}
		return GlobMatch(pat, base, false)
	}
	positive, hit := false, false
	for _, g := range cfg.globs {
		if strings.HasPrefix(g, "!") {
			if match(g[1:]) {
				return false
			}
			continue
		}
		positive = true
		if match(g) {
			hit = true
		}
	}
	return isDir || !positive || hit
}

func anyGlob(globs []string, name string) bool {
	for _, g := range globs {
		if GlobMatch(g, name, false) {
			return true
		}
	}
	return false
}

func runGrep(ctx context.Context, env *Env, cfg *grepCfg, paths []string) int {
	var re *regexp.Regexp
	if !cfg.filesOnly {
		var err error
		if re, err = cfg.compile(); err != nil {
			return env.usage(cfg.name, "invalid pattern: %v", err)
		}
	}
	implicitDot := false
	if len(paths) == 0 {
		if cfg.recursive {
			paths, implicitDot = []string{"."}, true
		} else {
			paths = []string{"-"}
		}
	}
	status := ExitOK
	errf := func(p string, err error) {
		status = ExitUsage
		if !cfg.noMessages {
			env.PathErr(cfg.name, p, err)
		}
	}
	var targets []target
	explicitDir := false
	for _, arg := range paths {
		if arg == "-" {
			targets = append(targets, target{"(standard input)", nil})
			continue
		}
		n, err := env.FS.Stat(ctx, env.Abs(arg))
		if err != nil {
			errf(arg, err)
			continue
		}
		if n.IsGitlink() {
			errf(arg, &vfs.SubmoduleError{Commit: n.OID})
			continue
		}
		if !n.IsDir() {
			if anyGlob(cfg.exclude, lastElem(arg)) {
				continue
			}
			targets = append(targets, target{arg, n})
			continue
		}
		if !cfg.recursive {
			status = ExitUsage
			if !cfg.noMessages {
				env.Errf(cfg.name, "%s: Is a directory", arg)
			}
			continue
		}
		explicitDir = true
		base := n.Path
		err = env.Walk(ctx, n, arg, WalkOpts{MaxDepth: -1}, func(e WalkEntry) error {
			if e.Depth == 0 {
				return nil
			}
			name := e.Node.Name
			if e.Node.IsDir() {
				if cfg.rg && !cfg.hidden && strings.HasPrefix(name, ".") || anyGlob(cfg.excludeDir, name) {
					return ErrSkipDir
				}
				if cfg.rg && !cfg.globOK(relPath(base, e.Node.Path), true) {
					return ErrSkipDir
				}
				return nil
			}
			if !e.Node.IsRegular() {
				return nil
			}
			if cfg.rg && !cfg.hidden && strings.HasPrefix(name, ".") {
				return nil
			}
			if len(cfg.include) > 0 && !anyGlob(cfg.include, name) || anyGlob(cfg.exclude, name) {
				return nil
			}
			if cfg.rg && !cfg.globOK(relPath(base, e.Node.Path), false) {
				return nil
			}
			disp := e.Display
			if implicitDot {
				disp = strings.TrimPrefix(disp, "./")
			}
			targets = append(targets, target{disp, e.Node})
			if len(targets) > env.Cfg.MaxFiles {
				return fmt.Errorf("too many files to scan (more than --max-files %d); narrow the path or use --include/-g", env.Cfg.MaxFiles)
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil || env.Stdout.Err() != nil {
				return ExitSigPipe
			}
			if isFSError(err) {
				errf(arg, err)
			} else {
				fmt.Fprintf(env.Stderr, "%s: %v\n", cfg.name, err)
				return ExitUsage
			}
		}
	}
	if len(targets) > env.Cfg.MaxFiles {
		fmt.Fprintf(env.Stderr, "%s: too many files to scan (%d > --max-files %d); narrow the path or use --include/-g\n", cfg.name, len(targets), env.Cfg.MaxFiles)
		return ExitUsage
	}

	if cfg.filesOnly {
		for _, t := range targets {
			if t.node != nil {
				env.Stdout.WriteString(t.display + "\n")
			}
		}
		if len(targets) == 0 {
			return ExitFail
		}
		return status
	}

	showName := len(targets) > 1 || explicitDir
	if cfg.noFilename {
		showName = false
	}
	if cfg.withFilename {
		showName = true
	}
	g := &grepRun{env: env, cfg: cfg, re: re, showName: showName}
	const chunk = 1000
	for start := 0; start < len(targets); start += chunk {
		end := start + chunk
		if end > len(targets) {
			end = len(targets)
		}
		var oids []object.OID
		for _, t := range targets[start:end] {
			if t.node != nil {
				oids = append(oids, t.node.OID)
			}
		}
		if start > 0 {
			env.Stdout.Flush() // surface a closed pipe (e.g. | head) before fetching more
			if env.Stdout.Err() != nil || ctx.Err() != nil {
				return ExitSigPipe
			}
		}
		fetchErr := env.Repo.Blobs(ctx, oids)
		if fetchErr != nil {
			env.PathErr(cfg.name, "", fetchErr)
			return ExitUsage
		}
		for _, t := range targets[start:end] {
			if ctx.Err() != nil || env.Stdout.Err() != nil {
				return ExitSigPipe
			}
			var data []byte
			var err error
			switch {
			case t.node == nil:
				data, err = io.ReadAll(env.Stdin)
			case fetchErr != nil:
				err = fetchErr
			default:
				data, err = env.FS.Read(ctx, t.node)
			}
			if err != nil {
				errf(t.display, err)
				continue
			}
			g.scan(t.display, data)
			if g.done {
				break
			}
		}
		if g.done {
			break
		}
	}
	if status != ExitOK && !(cfg.quiet && g.matched) {
		return ExitUsage
	}
	if g.matched {
		return ExitOK
	}
	return ExitFail
}

func lastElem(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func relPath(base, p string) string {
	r := strings.TrimPrefix(strings.TrimPrefix(p, base), "/")
	return r
}

type grepRun struct {
	env        *Env
	cfg        *grepCfg
	re         *regexp.Regexp
	showName   bool
	matched    bool
	printedAny bool
	done       bool
}

func (g *grepRun) prefix(name string, lineNo int, sep byte) string {
	var b strings.Builder
	if g.showName {
		b.WriteString(name)
		b.WriteByte(sep)
	}
	if g.cfg.lineNum {
		b.WriteString(strconv.Itoa(lineNo))
		b.WriteByte(sep)
	}
	return b.String()
}

func (g *grepRun) scan(name string, data []byte) {
	cfg, out := g.cfg, g.env.Stdout
	binary := !cfg.text && IsBinary(data)
	if binary && cfg.skipBinary {
		if cfg.listNon {
			out.WriteString(name + "\n")
		}
		return
	}
	lines := splitLines(data)
	selected := 0
	lastPrinted := -1
	afterLeft := 0
	contextOn := cfg.after > 0 || cfg.before > 0
	printLine := func(i int, sep byte) {
		if contextOn && lastPrinted >= 0 && i > lastPrinted+1 || contextOn && lastPrinted < 0 && g.printedAny {
			out.WriteString("--\n")
		}
		out.WriteString(g.prefix(name, i+1, sep))
		out.Write(lines[i])
		out.WriteString("\n")
		lastPrinted = i
		g.printedAny = true
	}
	quietScan := cfg.count || cfg.listMatch || cfg.listNon || cfg.quiet
	for i, ln := range lines {
		if i&1023 == 0 && (g.env.Stdout.Err() != nil) {
			g.done = true
			return
		}
		hit := g.re.Match(ln) != cfg.invert
		if !hit {
			if !quietScan && !binary && afterLeft > 0 {
				printLine(i, '-')
				afterLeft--
			}
			continue
		}
		selected++
		g.matched = true
		if cfg.quiet {
			g.done = true
			return
		}
		if cfg.listMatch {
			break
		}
		if !quietScan && !binary {
			if cfg.onlyMatching && !cfg.invert {
				for _, m := range g.re.FindAll(ln, -1) {
					if len(m) == 0 {
						continue
					}
					out.WriteString(g.prefix(name, i+1, ':'))
					out.Write(m)
					out.WriteString("\n")
				}
				lastPrinted = i
				g.printedAny = true
			} else {
				for j := max(lastPrinted+1, i-cfg.before); j < i; j++ {
					printLine(j, '-')
				}
				printLine(i, ':')
				afterLeft = cfg.after
			}
		}
		if cfg.maxCount > 0 && selected >= cfg.maxCount {
			break
		}
	}
	switch {
	case cfg.listMatch:
		if selected > 0 {
			out.WriteString(name + "\n")
		}
	case cfg.listNon:
		if selected == 0 {
			out.WriteString(name + "\n")
		}
	case cfg.count:
		if cfg.rg && selected == 0 {
			break
		}
		if g.showName {
			out.Printf("%s:%d\n", name, selected)
		} else {
			out.Printf("%d\n", selected)
		}
	case binary && selected > 0 && !cfg.quiet:
		out.Printf("Binary file %s matches\n", name)
	}
}

var _ = bytes.NewReader
