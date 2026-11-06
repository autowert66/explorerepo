// Package builtins implements the in-process command set available inside the
// embedded shell. Everything operates on the read-only virtual tree.
package builtins

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"explorerepo/internal/repo"
	"explorerepo/internal/vfs"
)

// Config carries per-invocation limits and context for commands.
type Config struct {
	MaxFiles int    // max files examined by one recursive content scan
	RepoName string // default destination name for pull
	Rev      string
}

// Env is what a command sees.
type Env struct {
	Stdin    io.Reader
	Stdout   *Out
	Stderr   io.Writer
	Dir      string
	FS       *vfs.FS
	Repo     *repo.Repo
	Cfg      *Config
	reported map[string]bool
	// Run executes another builtin (used by xargs / find -exec).
	Run func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) int
}

// Cmd is a builtin; it returns the exit status.
type Cmd func(ctx context.Context, env *Env, args []string) int

type Entry struct {
	Name    string
	Summary string
	Fn      Cmd
}

var registry = map[string]Entry{}

func register(name, summary string, fn Cmd) { registry[name] = Entry{name, summary, fn} }

// Lookup finds a command by name.
func Lookup(name string) (Entry, bool) { e, ok := registry[name]; return e, ok }

// Names lists all command names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Out is a buffered writer with a sticky error; commands stop quietly once the
// reader side of a pipe has gone away.
type Out struct {
	w   *bufio.Writer
	err error
}

func NewOut(w io.Writer) *Out { return &Out{w: bufio.NewWriterSize(w, 32*1024)} }

func (o *Out) Write(p []byte) (int, error) {
	if o.err != nil {
		return 0, o.err
	}
	n, err := o.w.Write(p)
	if err != nil {
		o.err = err
	}
	return n, err
}

func (o *Out) WriteString(s string)      { o.Write([]byte(s)) }
func (o *Out) Printf(f string, a ...any) { fmt.Fprintf(o, f, a...) }
func (o *Out) Println(a ...any)          { fmt.Fprintln(o, a...) }

// Err reports a sticky write error (broken pipe, truncated output...).
func (o *Out) Err() error { return o.err }

func (o *Out) Flush() error {
	if err := o.w.Flush(); err != nil && o.err == nil {
		o.err = err
	}
	return o.err
}

// status codes
const (
	ExitOK       = 0
	ExitFail     = 1
	ExitUsage    = 2
	ExitSigPipe  = 141
	ExitNotFound = 127
)

// Abs resolves a command-line path against the working directory.
func (e *Env) Abs(p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return path.Join(e.Dir, p)
}

func isFSError(err error) bool {
	var se *vfs.SubmoduleError
	return errors.Is(err, fs.ErrNotExist) || errors.As(err, &se) ||
		err == vfs.ErrIsDir || err == vfs.ErrNotDir || err == vfs.ErrLoop || err == vfs.ErrOutside || err == vfs.ErrReadOnly
}

// Errf prints "cmd: message".
func (e *Env) Errf(cmd, format string, a ...any) {
	fmt.Fprintf(e.Stderr, "%s: %s\n", cmd, fmt.Sprintf(format, a...))
}

// PathErr reports an error about a path in coreutils style. Network/budget
// errors are reported once as "explorerepo: ..." since they are not about the path.
func (e *Env) PathErr(cmd, p string, err error) {
	if isFSError(err) {
		e.Errf(cmd, "%s: %s", p, err)
		return
	}
	if e.reported == nil {
		e.reported = map[string]bool{}
	}
	if !e.reported[err.Error()] {
		e.reported[err.Error()] = true
		fmt.Fprintf(e.Stderr, "explorerepo: %s\n", err)
	}
}

// usage prints an option error and returns exit 2.
func (e *Env) usage(cmd, format string, a ...any) int {
	e.Errf(cmd, format, a...)
	return ExitUsage
}

// Unsupported prints a uniform message for unsupported features.
func (e *Env) Unsupported(cmd, what, hint string) int {
	msg := fmt.Sprintf("%s is not supported", what)
	if hint != "" {
		msg += " (" + hint + ")"
	}
	e.Errf(cmd, "%s", msg)
	return ExitUsage
}

// IsBinary applies git's rule: a NUL in the first 8000 bytes.
func IsBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// ---- option parsing ----

// Opt describes one option. Key is how Parsed reports it.
type Opt struct {
	Short byte   // 'n'
	Long  string // "max-count"
	Arg   bool
	Key   string // defaults to string(Short) or Long
}

func (o Opt) key() string {
	if o.Key != "" {
		return o.Key
	}
	if o.Short != 0 {
		return string(o.Short)
	}
	return o.Long
}

type Parsed struct {
	vals map[string][]string
	Args []string
}

func (p *Parsed) Has(k string) bool      { _, ok := p.vals[k]; return ok }
func (p *Parsed) Vals(k string) []string { return p.vals[k] }
func (p *Parsed) Val(k string) string {
	v := p.vals[k]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// ParseOpts parses POSIX/GNU-style options (clustered shorts, -n5, -n 5,
// --long=v, --, options after operands). numeric, if non-empty, names the key
// that a bare -NUM maps to (head/tail style).
func ParseOpts(args []string, opts []Opt, numeric string) (*Parsed, error) {
	p := &Parsed{vals: map[string][]string{}}
	short := map[byte]Opt{}
	long := map[string]Opt{}
	for _, o := range opts {
		if o.Short != 0 {
			short[o.Short] = o
		}
		if o.Long != "" {
			long[o.Long] = o
		}
	}
	unsupported := func(flag string) error {
		var names []string
		for _, o := range opts {
			if o.Short != 0 {
				names = append(names, "-"+string(o.Short))
			}
			if o.Long != "" {
				names = append(names, "--"+o.Long)
			}
		}
		return fmt.Errorf("unsupported option '%s' (supported: %s)", flag, strings.Join(names, " "))
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			p.Args = append(p.Args, args[i+1:]...)
			return p, nil
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			o, ok := long[name]
			if !ok {
				return nil, unsupported(a)
			}
			if o.Arg {
				if !hasVal {
					i++
					if i >= len(args) {
						return nil, fmt.Errorf("option '--%s' requires an argument", name)
					}
					val = args[i]
				}
				p.vals[o.key()] = append(p.vals[o.key()], val)
			} else {
				if hasVal {
					return nil, fmt.Errorf("option '--%s' doesn't allow an argument", name)
				}
				p.vals[o.key()] = append(p.vals[o.key()], "")
			}
		case len(a) > 1 && a[0] == '-':
			if numeric != "" && a[1] >= '0' && a[1] <= '9' {
				p.vals[numeric] = append(p.vals[numeric], a[1:])
				continue
			}
			for j := 1; j < len(a); j++ {
				o, ok := short[a[j]]
				if !ok {
					return nil, unsupported("-" + string(a[j]))
				}
				if !o.Arg {
					p.vals[o.key()] = append(p.vals[o.key()], "")
					continue
				}
				val := a[j+1:]
				if val == "" {
					i++
					if i >= len(args) {
						return nil, fmt.Errorf("option requires an argument -- '%c'", a[j])
					}
					val = args[i]
				}
				p.vals[o.key()] = append(p.vals[o.key()], val)
				break
			}
		default:
			p.Args = append(p.Args, a)
		}
	}
	return p, nil
}

// parse is the common wrapper: prints errors with the command name.
func (e *Env) parse(cmd string, args []string, opts []Opt, numeric string) (*Parsed, bool) {
	p, err := ParseOpts(args, opts, numeric)
	if err != nil {
		e.Errf(cmd, "%v", err)
		return nil, false
	}
	return p, true
}

// ---- glob matching ----

// GlobMatch matches an fnmatch-style pattern (* ? [..]) against s. When
// pathMode is false, * and ? also match '/' (as in find -path); the match is
// always anchored.
func GlobMatch(pattern, s string, caseFold bool) bool {
	return globRec([]rune(pattern), []rune(s), caseFold)
}

func globRec(p, s []rune, fold bool) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for len(p) > 0 && p[0] == '*' {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if globRec(p, s[i:], fold) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		case '[':
			if len(s) == 0 {
				return false
			}
			end := 1
			if end < len(p) && (p[end] == '!' || p[end] == '^') {
				end++
			}
			if end < len(p) && p[end] == ']' {
				end++
			}
			for end < len(p) && p[end] != ']' {
				end++
			}
			if end >= len(p) { // no closing bracket: literal '['
				if !eqRune(p[0], s[0], fold) {
					return false
				}
				p, s = p[1:], s[1:]
				continue
			}
			set := p[1:end]
			neg := len(set) > 0 && (set[0] == '!' || set[0] == '^')
			if neg {
				set = set[1:]
			}
			ok := false
			for k := 0; k < len(set); k++ {
				if k+2 < len(set) && set[k+1] == '-' {
					lo, hi, c := set[k], set[k+2], s[0]
					if fold {
						lo, hi, c = lower(lo), lower(hi), lower(c)
					}
					if c >= lo && c <= hi {
						ok = true
					}
					k += 2
				} else if eqRune(set[k], s[0], fold) {
					ok = true
				}
			}
			if ok == neg {
				return false
			}
			p, s = p[end+1:], s[1:]
		case '\\':
			if len(p) > 1 {
				p = p[1:]
			}
			fallthrough
		default:
			if len(s) == 0 || !eqRune(p[0], s[0], fold) {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

func lower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

func eqRune(a, b rune, fold bool) bool {
	if a == b {
		return true
	}
	return fold && lower(a) == lower(b)
}
