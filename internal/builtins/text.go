package builtins

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func init() {
	register("sort", "sort lines (-r -n -u -f -k N -t C)", sortCmd)
	register("uniq", "collapse adjacent duplicate lines (-c -d -u -i)", uniqCmd)
	register("cut", "select fields/characters (-d -f -c)", cutCmd)
	register("tr", "translate or delete characters (-d -s -c)", trCmd)
	register("xargs", "build commands from stdin (-n -I -0)", xargsCmd)
	register("basename", "strip directory and suffix", basenameCmd)
	register("dirname", "strip last path component", dirnameCmd)
	register("tee", "copy stdin to stdout (files are read-only)", teeCmd)
	register("echo", "print arguments (for xargs/find -exec)", echoCmd)
	register("printf", "format and print arguments (for xargs/find -exec)", printfCmd)
}

// readInputs returns the lines of all named inputs (stdin when none).
func (e *Env) readInputs(ctx context.Context, cmd string, names []string) ([][]byte, int) {
	if len(names) == 0 {
		names = []string{"-"}
	}
	status := ExitOK
	var lines [][]byte
	for _, r := range e.readFiles(ctx, names) {
		if r.Err != nil {
			e.PathErr(cmd, r.Name, r.Err)
			status = ExitFail
			continue
		}
		lines = append(lines, splitLines(r.Data)...)
	}
	return lines, status
}

// ---- sort ----

var numPrefix = regexp.MustCompile(`^\s*[-+]?(\d+\.?\d*|\.\d+)`)

func numValue(s string, human bool) float64 {
	m := numPrefix.FindString(s)
	if m == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(m), 64)
	if human {
		rest := strings.TrimSpace(s[len(m):])
		if rest != "" {
			if i := strings.IndexByte("KMGTPE", strings.ToUpper(rest)[0]); i >= 0 {
				v *= math.Pow(1024, float64(i+1))
			}
		}
	}
	return v
}

type sortSpec struct {
	start, end int // 1-based fields; end 0 = through last
	numeric    bool
	human      bool
	fold       bool
	reverse    bool
}

func (k sortSpec) extract(line string, sep string) string {
	if k.start == 0 {
		return line
	}
	var fields []string
	join := " "
	if sep != "" {
		fields, join = strings.Split(line, sep), sep
	} else {
		fields = strings.Fields(line)
	}
	if k.start > len(fields) {
		return ""
	}
	end := k.end
	if end == 0 || end > len(fields) {
		end = len(fields)
	}
	return strings.Join(fields[k.start-1:end], join)
}

func parseKey(s string, def sortSpec) (sortSpec, error) {
	k := def
	i := len(s)
	for i > 0 && strings.ContainsRune("nrfhb", rune(s[i-1])) {
		switch s[i-1] {
		case 'n':
			k.numeric = true
		case 'r':
			k.reverse = true
		case 'f':
			k.fold = true
		case 'h':
			k.human = true
		}
		i--
	}
	s = s[:i]
	a, b, hasEnd := strings.Cut(s, ",")
	a, _, _ = strings.Cut(a, ".")
	b, _, _ = strings.Cut(b, ".")
	var err error
	if k.start, err = strconv.Atoi(a); err != nil || k.start < 1 {
		return k, fmt.Errorf("invalid key %q", s)
	}
	if hasEnd {
		if k.end, err = strconv.Atoi(b); err != nil || k.end < k.start {
			return k, fmt.Errorf("invalid key %q", s)
		}
	}
	return k, nil
}

func sortCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("sort", args, []Opt{
		{Short: 'r'}, {Short: 'n'}, {Short: 'u'}, {Short: 'f'}, {Short: 's'}, {Short: 'h'}, {Short: 'b'},
		{Short: 'k', Arg: true}, {Short: 't', Arg: true}, {Short: 'V'}, {Short: 'o', Arg: true}, {Short: 'z'},
	}, "")
	if !ok {
		return ExitUsage
	}
	if p.Has("V") || p.Has("z") {
		return env.Unsupported("sort", "-V/-z", "")
	}
	if p.Has("o") {
		return env.Unsupported("sort", "-o", "the filesystem is read-only; use shell redirection to stdout")
	}
	def := sortSpec{numeric: p.Has("n"), human: p.Has("h"), fold: p.Has("f"), reverse: p.Has("r")}
	keys := []sortSpec{def}
	if ks := p.Vals("k"); len(ks) > 0 {
		keys = nil
		for _, ksp := range ks {
			k, err := parseKey(ksp, sortSpec{})
			if err != nil {
				return env.usage("sort", "%v", err)
			}
			if !k.numeric && !k.reverse && !k.fold && !k.human {
				k.numeric, k.human, k.fold, k.reverse = def.numeric, def.human, def.fold, def.reverse
			}
			keys = append(keys, k)
		}
	}
	sep := p.Val("t")
	raw, status := env.readInputs(ctx, "sort", p.Args)
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = string(l)
	}
	cmpKeys := func(a, b string) int {
		for _, k := range keys {
			ka, kb := k.extract(a, sep), k.extract(b, sep)
			c := 0
			switch {
			case k.numeric || k.human:
				va, vb := numValue(ka, k.human), numValue(kb, k.human)
				if va < vb {
					c = -1
				} else if va > vb {
					c = 1
				}
			default:
				if k.fold {
					ka, kb = strings.ToLower(ka), strings.ToLower(kb)
				}
				c = strings.Compare(ka, kb)
			}
			if k.reverse {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return 0
	}
	unique := p.Has("u")
	less := func(i, j int) bool {
		c := cmpKeys(lines[i], lines[j])
		if c == 0 && !p.Has("s") && !unique {
			c = strings.Compare(lines[i], lines[j])
			if keys[0].reverse {
				c = -c
			}
		}
		return c < 0
	}
	sort.SliceStable(lines, less)
	for i, l := range lines {
		if unique && i > 0 && cmpKeys(lines[i-1], l) == 0 {
			continue
		}
		env.Stdout.WriteString(l + "\n")
		if env.Stdout.Err() != nil {
			return ExitSigPipe
		}
	}
	return status
}

// ---- uniq ----

func uniqCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("uniq", args, []Opt{{Short: 'c'}, {Short: 'd'}, {Short: 'u'}, {Short: 'i'}}, "")
	if !ok {
		return ExitUsage
	}
	if len(p.Args) > 1 {
		return env.Unsupported("uniq", "an output file operand", "the filesystem is read-only")
	}
	lines, status := env.readInputs(ctx, "uniq", p.Args)
	key := func(b []byte) string {
		if p.Has("i") {
			return strings.ToLower(string(b))
		}
		return string(b)
	}
	flush := func(line []byte, n int) {
		if p.Has("d") && n < 2 || p.Has("u") && n != 1 {
			return
		}
		if p.Has("c") {
			env.Stdout.Printf("%7d %s\n", n, line)
		} else {
			env.Stdout.Printf("%s\n", line)
		}
	}
	var cur []byte
	n := 0
	for _, l := range lines {
		if n > 0 && key(l) == key(cur) {
			n++
			continue
		}
		if n > 0 {
			flush(cur, n)
		}
		cur, n = l, 1
	}
	if n > 0 {
		flush(cur, n)
	}
	return status
}

// ---- cut ----

type rng struct{ lo, hi int } // 1-based inclusive; hi 0 = open

func parseList(s string) ([]rng, error) {
	var out []rng
	for _, part := range strings.Split(s, ",") {
		a, b, isRange := strings.Cut(part, "-")
		var r rng
		var err error
		if !isRange {
			if r.lo, err = strconv.Atoi(a); err != nil || r.lo < 1 {
				return nil, fmt.Errorf("invalid field value %q", part)
			}
			r.hi = r.lo
		} else {
			r.lo = 1
			if a != "" {
				if r.lo, err = strconv.Atoi(a); err != nil || r.lo < 1 {
					return nil, fmt.Errorf("invalid range %q", part)
				}
			}
			if b != "" {
				if r.hi, err = strconv.Atoi(b); err != nil || r.hi < r.lo {
					return nil, fmt.Errorf("invalid range %q", part)
				}
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func inList(rs []rng, i int) bool {
	for _, r := range rs {
		if i >= r.lo && (r.hi == 0 || i <= r.hi) {
			return true
		}
	}
	return false
}

func cutCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("cut", args, []Opt{{Short: 'd', Arg: true}, {Short: 'f', Arg: true}, {Short: 'c', Arg: true}, {Short: 'b', Arg: true}, {Short: 's'}}, "")
	if !ok {
		return ExitUsage
	}
	delim := "\t"
	if p.Has("d") {
		delim = p.Val("d")
		if len([]rune(delim)) != 1 {
			return env.usage("cut", "the delimiter must be a single character")
		}
	}
	spec, chars := p.Val("f"), false
	if spec == "" {
		spec, chars = p.Val("c"), true
		if spec == "" {
			spec = p.Val("b")
		}
	}
	if spec == "" {
		return env.usage("cut", "you must specify a list of bytes, characters, or fields")
	}
	list, err := parseList(spec)
	if err != nil {
		return env.usage("cut", "%v", err)
	}
	lines, status := env.readInputs(ctx, "cut", p.Args)
	for _, l := range lines {
		s := string(l)
		if chars {
			var b strings.Builder
			for i, r := range []rune(s) {
				if inList(list, i+1) {
					b.WriteRune(r)
				}
			}
			env.Stdout.WriteString(b.String() + "\n")
			continue
		}
		if !strings.Contains(s, delim) {
			if !p.Has("s") {
				env.Stdout.WriteString(s + "\n")
			}
			continue
		}
		var out []string
		for i, f := range strings.Split(s, delim) {
			if inList(list, i+1) {
				out = append(out, f)
			}
		}
		env.Stdout.WriteString(strings.Join(out, delim) + "\n")
		if env.Stdout.Err() != nil {
			return ExitSigPipe
		}
	}
	return status
}

// ---- tr ----

var trClasses = map[string]func(rune) bool{
	"alpha": unicode.IsLetter, "digit": unicode.IsDigit, "alnum": func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) },
	"upper": unicode.IsUpper, "lower": unicode.IsLower, "space": unicode.IsSpace, "punct": unicode.IsPunct,
	"blank": func(r rune) bool { return r == ' ' || r == '\t' },
}

// expandSet expands ranges, escapes and [:class:] into a rune list.
func expandSet(s string) ([]rune, error) {
	rs := []rune(s)
	var out []rune
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if c == '[' && i+1 < len(rs) && rs[i+1] == ':' {
			// A class is "[:name:]", so the closing ":]" must sit at least
			// past the opener. "[:]" and "[:x" are not classes: fall through
			// and let the bytes be matched literally.
			end := strings.Index(string(rs[i:]), ":]")
			if end > 2 {
				name := string(rs[i+2 : i+end])
				fn, ok := trClasses[name]
				if !ok {
					return nil, fmt.Errorf("invalid character class '[:%s:]'", name)
				}
				for r := rune(0); r < 256; r++ {
					if fn(r) {
						out = append(out, r)
					}
				}
				i += end + 1
				continue
			}
		}
		if c == '\\' && i+1 < len(rs) {
			i++
			switch rs[i] {
			case 'n':
				c = '\n'
			case 't':
				c = '\t'
			case 'r':
				c = '\r'
			case '0':
				c = 0
			default:
				c = rs[i]
			}
		}
		if i+2 < len(rs) && rs[i+1] == '-' {
			hi := rs[i+2]
			if hi < c {
				return nil, fmt.Errorf("range-endpoints of '%c-%c' are in reverse collating sequence order", c, hi)
			}
			for r := c; r <= hi; r++ {
				out = append(out, r)
			}
			i += 2
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func trCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("tr", args, []Opt{{Short: 'd'}, {Short: 's'}, {Short: 'c'}}, "")
	if !ok {
		return ExitUsage
	}
	del, squeeze, compl := p.Has("d"), p.Has("s"), p.Has("c")
	if len(p.Args) == 0 || len(p.Args) > 2 || (!del && !squeeze && len(p.Args) != 2) {
		return env.usage("tr", "usage: tr [-d] [-s] [-c] SET1 [SET2]")
	}
	set1, err := expandSet(p.Args[0])
	if err != nil {
		return env.usage("tr", "%v", err)
	}
	var set2 []rune
	if len(p.Args) == 2 {
		if set2, err = expandSet(p.Args[1]); err != nil {
			return env.usage("tr", "%v", err)
		}
	}
	in1 := map[rune]bool{}
	for _, r := range set1 {
		in1[r] = true
	}
	member := func(r rune) bool { return in1[r] != compl }
	mapping := map[rune]rune{}
	if !del && len(set2) > 0 && !compl {
		for i, r := range set1 {
			j := i
			if j >= len(set2) {
				j = len(set2) - 1
			}
			mapping[r] = set2[j]
		}
	}
	sq := map[rune]bool{}
	if squeeze {
		src := set1
		if len(set2) > 0 && !del {
			src = set2
		}
		for _, r := range src {
			sq[r] = true
		}
	}
	br := bufio.NewReader(env.Stdin)
	var last rune = -1
	for {
		r, _, err := br.ReadRune()
		if err != nil {
			break
		}
		if del && member(r) {
			continue
		}
		if m, ok := mapping[r]; ok {
			r = m
		} else if compl && !del && len(set2) > 0 && member(r) {
			r = set2[len(set2)-1]
		}
		if squeeze && sq[r] && r == last {
			continue
		}
		last = r
		env.Stdout.WriteString(string(r))
		if env.Stdout.Err() != nil {
			return ExitSigPipe
		}
	}
	return ExitOK
}

// ---- xargs ----

func splitShellWords(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case unicode.IsSpace(r):
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unmatched %c quote; by default quotes are special to xargs unless you use the -0 option", quote)
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

func xargsCmd(ctx context.Context, env *Env, args []string) int {
	maxArgs, repl, nul := 0, "", false
	i := 0
loop:
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			break loop
		case a == "-0" || a == "--null":
			nul = true
		case a == "-r" || a == "--no-run-if-empty" || a == "-t" || a == "-p":
		case a == "-n" || a == "-I" || a == "-P" || a == "-L":
			if i+1 >= len(args) {
				return env.usage("xargs", "option requires an argument -- '%s'", a)
			}
			i++
			if err := xargsNum(env, a, args[i], &maxArgs, &repl); err != nil {
				return env.usage("xargs", "%v", err)
			}
		case strings.HasPrefix(a, "-n") || strings.HasPrefix(a, "-I") || strings.HasPrefix(a, "-P") || strings.HasPrefix(a, "-L"):
			if err := xargsNum(env, a[:2], a[2:], &maxArgs, &repl); err != nil {
				return env.usage("xargs", "%v", err)
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			return env.Unsupported("xargs", "option "+a, "supported: -n N, -I R, -0, -r, -P N (ignored)")
		default:
			break loop
		}
	}
	cmd := args[i:]
	if len(cmd) == 0 {
		cmd = []string{"echo"}
	}
	data, err := io.ReadAll(env.Stdin)
	if err != nil {
		return env.usage("xargs", "%v", err)
	}
	var items []string
	switch {
	case nul:
		for _, f := range bytes.Split(data, []byte{0}) {
			if len(f) > 0 {
				items = append(items, string(f))
			}
		}
	case repl != "":
		for _, l := range splitLines(data) {
			if s := strings.TrimSpace(string(l)); s != "" {
				items = append(items, s)
			}
		}
	default:
		if items, err = splitShellWords(string(data)); err != nil {
			return env.usage("xargs", "%v", err)
		}
	}
	if len(items) == 0 {
		return ExitOK
	}
	run := func(argv []string) bool {
		env.Stdout.Flush()
		return env.Run(ctx, argv, strings.NewReader(""), env.Stdout) == 0
	}
	failed := false
	switch {
	case repl != "":
		for _, it := range items {
			argv := make([]string, len(cmd))
			for j, c := range cmd {
				argv[j] = strings.ReplaceAll(c, repl, it)
			}
			if !run(argv) {
				failed = true
			}
			if ctx.Err() != nil || env.Stdout.Err() != nil {
				return ExitSigPipe
			}
		}
	default:
		step := len(items)
		if maxArgs > 0 {
			step = maxArgs
		}
		for s := 0; s < len(items); s += step {
			e := s + step
			if e > len(items) {
				e = len(items)
			}
			if !run(append(append([]string{}, cmd...), items[s:e]...)) {
				failed = true
			}
			if ctx.Err() != nil || env.Stdout.Err() != nil {
				return ExitSigPipe
			}
		}
	}
	if failed {
		return 123
	}
	return ExitOK
}

func xargsNum(env *Env, flag, val string, maxArgs *int, repl *string) error {
	switch flag {
	case "-n", "-L":
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("invalid number %q for %s", val, flag)
		}
		*maxArgs = n
	case "-I":
		*repl = val
	}
	return nil
}

// ---- small path utilities ----

func basenameCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("basename", args, []Opt{{Short: 'a'}, {Short: 's', Arg: true}}, "")
	if !ok {
		return ExitUsage
	}
	names, suffix := p.Args, p.Val("s")
	if len(names) == 0 {
		return env.usage("basename", "missing operand")
	}
	if !p.Has("a") && !p.Has("s") && len(names) == 2 {
		names, suffix = names[:1], names[1]
	} else if !p.Has("a") && !p.Has("s") && len(names) > 2 {
		return env.usage("basename", "extra operand '%s'", names[2])
	}
	for _, n := range names {
		b := path.Base(strings.TrimRight(n, "/"))
		if n == "" {
			b = ""
		} else if strings.Trim(n, "/") == "" {
			b = "/"
		}
		if suffix != "" && b != suffix {
			b = strings.TrimSuffix(b, suffix)
		}
		env.Stdout.WriteString(b + "\n")
	}
	return ExitOK
}

func dirnameCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("dirname", args, nil, "")
	if !ok {
		return ExitUsage
	}
	if len(p.Args) == 0 {
		return env.usage("dirname", "missing operand")
	}
	for _, n := range p.Args {
		env.Stdout.WriteString(path.Dir(n) + "\n")
	}
	return ExitOK
}

func teeCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("tee", args, []Opt{{Short: 'a'}, {Short: 'i'}}, "")
	if !ok {
		return ExitUsage
	}
	status := ExitOK
	for _, f := range p.Args {
		if f != "/dev/null" {
			env.Errf("tee", "%s: read-only file system", f)
			status = ExitFail
		}
	}
	io.Copy(env.Stdout, env.Stdin)
	return status
}

func echoCmd(ctx context.Context, env *Env, args []string) int {
	nl := true
	if len(args) > 0 && args[0] == "-n" {
		nl, args = false, args[1:]
	}
	env.Stdout.WriteString(strings.Join(args, " "))
	if nl {
		env.Stdout.WriteString("\n")
	}
	return ExitOK
}

// ---- printf ----

// printfConv is one parsed conversion (everything after '%').
type printfConv struct {
	verb      byte
	flags     string
	width     int // -1 = unset
	prec      int // -1 = unset
	widthStar bool
	precStar  bool
}

// printfSeg is either literal text or a single conversion.
type printfSeg struct {
	literal string
	conv    *printfConv
}

// parsePrintfFormat splits a format into literal and conversion segments,
// decoding escapes in the literals. stop reports a \c escape, after which the
// rest of the format (and the output) is dropped.
func parsePrintfFormat(format string) (segs []printfSeg, stop bool) {
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			segs = append(segs, printfSeg{literal: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(format); {
		switch format[i] {
		case '\\':
			out, n, halt := decodeEscape(format[i:])
			if halt {
				flush()
				return segs, true
			}
			lit.WriteString(out)
			i += n
		case '%':
			if conv, n, ok := parsePrintfConv(format[i:]); ok {
				flush()
				segs = append(segs, printfSeg{conv: conv})
				i += n
				continue
			}
			lit.WriteByte('%')
			i++
		default:
			lit.WriteByte(format[i])
			i++
		}
	}
	flush()
	return segs, false
}

// parsePrintfConv parses "%[flags][width][.prec]verb"; n is the bytes consumed.
func parsePrintfConv(s string) (*printfConv, int, bool) {
	c := &printfConv{width: -1, prec: -1}
	i := 1
	for i < len(s) && strings.IndexByte("-+ #0", s[i]) >= 0 {
		c.flags += string(s[i])
		i++
	}
	if i < len(s) && s[i] == '*' {
		c.widthStar = true
		i++
	} else {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			if c.width < 0 {
				c.width = 0
			}
			c.width = c.width*10 + int(s[i]-'0')
			i++
		}
	}
	if i < len(s) && s[i] == '.' {
		i++
		if i < len(s) && s[i] == '*' {
			c.precStar = true
			i++
		} else {
			c.prec = 0
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				c.prec = c.prec*10 + int(s[i]-'0')
				i++
			}
		}
	}
	if i >= len(s) {
		return nil, 0, false
	}
	c.verb = s[i]
	i++
	if !strings.ContainsRune("diouxXeEfFgGaAcsbq%", rune(c.verb)) {
		return nil, 0, false
	}
	return c, i, true
}

// decodeEscape decodes one backslash escape at the start of s (s[0] == '\\').
// It returns the decoded text, the bytes consumed, and whether \c asked to
// stop all further output.
func decodeEscape(s string) (string, int, bool) {
	if len(s) < 2 {
		return "\\", 1, false
	}
	switch c := s[1]; c {
	case '\\', '\'', '"', '?':
		return string(c), 2, false
	case 'a':
		return "\a", 2, false
	case 'b':
		return "\b", 2, false
	case 'c':
		return "", 2, true
	case 'e', 'E':
		return "\x1b", 2, false
	case 'f':
		return "\f", 2, false
	case 'n':
		return "\n", 2, false
	case 'r':
		return "\r", 2, false
	case 't':
		return "\t", 2, false
	case 'v':
		return "\v", 2, false
	case '0', '1', '2', '3', '4', '5', '6', '7':
		j, val := 1, 0
		for j < len(s) && j <= 3 && s[j] >= '0' && s[j] <= '7' {
			val = val*8 + int(s[j]-'0')
			j++
		}
		return string([]byte{byte(val)}), j, false
	case 'x', 'u', 'U':
		max := 2
		switch c {
		case 'u':
			max = 4
		case 'U':
			max = 8
		}
		j, val, n := 2, 0, 0
		for j < len(s) && n < max && isHex(s[j]) {
			val = val*16 + hexVal(s[j])
			j++
			n++
		}
		if n == 0 {
			return "\\" + string(c), 2, false
		}
		if c == 'x' {
			return string([]byte{byte(val)}), j, false
		}
		return string(rune(val)), j, false
	default:
		return "\\" + string(c), 2, false
	}
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}

// expandPrintfEscapes decodes the escapes of a %b argument; stop reports \c.
func expandPrintfEscapes(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		out, n, stop := decodeEscape(s[i:])
		b.WriteString(out)
		i += n
		if stop {
			return b.String(), true
		}
	}
	return b.String(), false
}

func printfCmd(ctx context.Context, env *Env, args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return env.usage("printf", "usage: printf FORMAT [ARGUMENTS...]")
	}
	segs, stop := parsePrintfFormat(args[0])
	rest := args[1:]
	ai := 0
	next := func() string {
		if ai < len(rest) {
			s := rest[ai]
			ai++
			return s
		}
		return ""
	}
	consumes := false
	for _, seg := range segs {
		if seg.conv != nil {
			consumes = true
			break
		}
	}
	status := ExitOK
	for {
		for _, seg := range segs {
			if seg.conv == nil {
				env.Stdout.WriteString(seg.literal)
				continue
			}
			if !printfEmit(env, seg.conv, next, &status) {
				return status
			}
			if env.Stdout.Err() != nil {
				return ExitSigPipe
			}
		}
		// coreutils reuses the format while arguments remain.
		if stop || !consumes || ai >= len(rest) {
			break
		}
	}
	return status
}

// maxPrintfWidth bounds a field width or precision. The value can come from
// the format string or an argument, and both padding and Go's format verbs
// allocate it directly.
const maxPrintfWidth = 1 << 20

// printfEmit formats one conversion, pulling its argument(s) via next. It
// returns false when a %b argument contained \c (stop all output).
func printfEmit(env *Env, c *printfConv, next func() string, status *int) bool {
	if c.verb == '%' {
		env.Stdout.WriteString("%")
		return true
	}
	conv := *c
	if conv.widthStar {
		w := printfAtoi(next())
		if w < 0 {
			w = -w
			if !strings.ContainsRune(conv.flags, '-') {
				conv.flags = "-" + conv.flags
			}
		}
		conv.width = w
	}
	if conv.precStar {
		if p := printfAtoi(next()); p < 0 {
			conv.prec = -1
		} else {
			conv.prec = p
		}
	}
	if conv.width > maxPrintfWidth || conv.prec > maxPrintfWidth {
		env.Errf("printf", "field width or precision too large")
		*status = ExitFail
		return false
	}
	arg := next()
	switch conv.verb {
	case 'b':
		s, stop := expandPrintfEscapes(arg)
		printfPad(env, &conv, s)
		return !stop
	case 'q':
		printfPad(env, &conv, shellQuote(arg))
	case 's':
		printfPad(env, &conv, arg)
	case 'c':
		s := ""
		if arg != "" {
			r, _ := utf8.DecodeRuneInString(arg)
			s = string(r)
		}
		printfPad(env, &conv, s)
	case 'd', 'i', 'o', 'u', 'x', 'X':
		n, ok := printfInt(arg)
		if !ok {
			env.Errf("printf", "%s: expected a numeric value", arg)
			*status = ExitFail
		}
		verb, farg := conv.verb, any(n)
		switch verb {
		case 'i':
			verb = 'd'
		case 'u':
			verb, farg = 'd', uint64(n)
		case 'o', 'x', 'X':
			farg = uint64(n)
		}
		fmt.Fprintf(env.Stdout, printfNumFormat(&conv, verb), farg)
	case 'e', 'E', 'f', 'F', 'g', 'G':
		f, ok := printfFloat(arg)
		if !ok {
			env.Errf("printf", "%s: expected a numeric value", arg)
			*status = ExitFail
		}
		fmt.Fprintf(env.Stdout, printfNumFormat(&conv, conv.verb), f)
	case 'a', 'A':
		env.Errf("printf", "%%%c: unsupported conversion", conv.verb)
		*status = ExitFail
	}
	return true
}

// printfPad applies precision (in bytes, like coreutils) and width to a string.
func printfPad(env *Env, c *printfConv, s string) {
	if c.prec >= 0 && len(s) > c.prec {
		s = s[:c.prec]
	}
	if c.width <= len(s) {
		env.Stdout.WriteString(s)
		return
	}
	pad := strings.Repeat(" ", c.width-len(s))
	if strings.IndexByte(c.flags, '-') >= 0 {
		env.Stdout.WriteString(s + pad)
		return
	}
	env.Stdout.WriteString(pad + s)
}

// printfNumFormat rebuilds a Go format verb for a numeric conversion.
func printfNumFormat(c *printfConv, verb byte) string {
	var b strings.Builder
	b.WriteByte('%')
	b.WriteString(c.flags)
	if c.width >= 0 {
		b.WriteString(strconv.Itoa(c.width))
	}
	if c.prec >= 0 {
		b.WriteByte('.')
		b.WriteString(strconv.Itoa(c.prec))
	}
	b.WriteByte(verb)
	return b.String()
}

func printfInt(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	if s[0] == '\'' || s[0] == '"' { // character constant, e.g. "'A"
		r, _, _, err := strconv.UnquoteChar(s[1:], 0)
		if err != nil {
			return 0, false
		}
		return int64(r), true
	}
	if n, err := strconv.ParseInt(s, 0, 64); err == nil {
		return n, true
	}
	if u, err := strconv.ParseUint(s, 0, 64); err == nil {
		return int64(u), true
	}
	return 0, false
}

func printfFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func printfAtoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// shellQuote renders s so it can be reused as a shell word (%q).
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("@%+=:,./-_", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
