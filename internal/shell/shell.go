// Package shell wires mvdan.cc/sh to the virtual filesystem and the builtin
// command set. Nothing here may touch the host filesystem or run real programs.
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync/atomic"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/autowert66/explorerepo/internal/builtins"
	"github.com/autowert66/explorerepo/internal/color"
	"github.com/autowert66/explorerepo/internal/repo"
	"github.com/autowert66/explorerepo/internal/vfs"
)

type Options struct {
	Repo      *repo.Repo
	FS        *vfs.FS
	Cfg       *builtins.Config
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	MaxOutput int64 // 0 = unlimited
	Color     bool  // Stdout is a human's terminal; style what is written straight to it
}

// limitWriter stops output once the cap is reached and cancels the run.
type limitWriter struct {
	w         io.Writer
	max       int64
	n         atomic.Int64
	truncated atomic.Bool
	cancel    context.CancelFunc
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.truncated.Load() {
		return 0, io.ErrClosedPipe
	}
	if l.max <= 0 {
		return l.w.Write(p)
	}
	total := l.n.Add(int64(len(p)))
	if total <= l.max {
		return l.w.Write(p)
	}
	room := int64(len(p)) - (total - l.max)
	if room > 0 {
		l.w.Write(p[:room])
	}
	if l.truncated.CompareAndSwap(false, true) {
		l.cancel()
	}
	return 0, io.ErrClosedPipe
}

// Result of a run.
type Result struct {
	Status    int
	Truncated bool
}

func truncNotice(max int64) string {
	return fmt.Sprintf("\n[explorerepo: output truncated at %s; use head/grep to narrow]\n", humanBytes(max))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

type runner struct {
	opt Options
	out *builtins.Out // not used directly; each command gets its own
	top io.Writer     // the run's own stdout, as opposed to a pipe or redirect
}

// color reports whether a command writing to w writes straight to a color
// terminal: in 'help; help | head' only the first is styled.
func (r *runner) color(w io.Writer) builtins.Styler {
	return builtins.Styler{On: r.opt.Color && w == r.top}
}

// exec runs a builtin with the given stdio and returns its status.
func (r *runner) exec(ctx context.Context, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if handled, status := probe(args, stdout, stderr); handled {
		return status
	}
	// `CMD --version` / `CMD --help`: agents probe with these, so answer
	// instead of failing on an unsupported option.
	if len(args) == 2 && (args[1] == "--version" || args[1] == "--help") {
		if e, ok := builtins.Lookup(args[0]); ok {
			if args[1] == "--help" {
				text, _ := builtins.Help([]string{e.Name})
				io.WriteString(stdout, r.color(stdout).Help(text))
			} else {
				fmt.Fprintf(stdout, "%s (explorerepo builtin)\n", e.Name)
			}
			return builtins.ExitOK
		}
	}
	ent, ok := builtins.Lookup(args[0])
	if !ok || strings.Contains(args[0], "/") {
		fmt.Fprintf(stderr, "explorerepo: %s: command not available (run 'help')\n", args[0])
		return builtins.ExitNotFound
	}
	out := builtins.NewOut(stdout)
	env := &builtins.Env{
		Stdin: stdin, Stdout: out, Stderr: stderr, Dir: dir,
		FS: r.opt.FS, Repo: r.opt.Repo, Cfg: r.opt.Cfg, Color: r.color(stdout),
		Run: func(ctx context.Context, a []string, in io.Reader, o io.Writer) int {
			return r.exec(ctx, dir, a, in, o, stderr)
		},
	}
	status := ent.Fn(ctx, env, args[1:])
	out.Flush()
	if err := out.Err(); err != nil && status == 0 {
		status = builtins.ExitSigPipe
	}
	return status
}

// RunCommand executes a single builtin with no shell parsing.
func RunCommand(ctx context.Context, opt Options, args []string) Result {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lw := &limitWriter{w: opt.Stdout, max: opt.MaxOutput, cancel: cancel}
	r := &runner{opt: opt, top: lw}
	status := r.exec(ctx, "/", args, opt.Stdin, lw, opt.Stderr)
	return finish(opt, lw, status)
}

func finish(opt Options, lw *limitWriter, status int) Result {
	if lw.truncated.Load() {
		if opt.Color {
			io.WriteString(opt.Stdout, color.Reset) // the cut may fall inside a style
		}
		io.WriteString(opt.Stderr, truncNotice(lw.max))
		return Result{Status: 0, Truncated: true}
	}
	return Result{Status: status}
}

// Run parses and executes a shell script.
func Run(ctx context.Context, opt Options, script string) Result {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		fmt.Fprintf(opt.Stderr, "explorerepo: syntax error: %v\n", err)
		return Result{Status: 2}
	}
	lw := &limitWriter{w: opt.Stdout, max: opt.MaxOutput, cancel: cancel}
	r := &runner{opt: opt, top: lw}
	info := opt.Repo.Info()
	env := expand.ListEnviron("PWD=/", "HOME=/", "PATH=", "REPO="+info.Repo, "REV="+info.Rev, "COMMIT="+info.Commit.String())

	ip, err := interp.New(
		interp.Env(env),
		interp.Dir("/"),
		interp.StdIO(opt.Stdin, lw, opt.Stderr),
		interp.CallHandler(r.call),
		interp.ExecHandlers(func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				hc := interp.HandlerCtx(ctx)
				st := r.exec(ctx, hc.Dir, args, hc.Stdin, hc.Stdout, hc.Stderr)
				if st == 0 {
					return nil
				}
				return interp.NewExitStatus(uint8(st))
			}
		}),
		interp.OpenHandler(r.open),
		interp.ReadDirHandler2(r.readDir),
		interp.StatHandler(r.stat),
		interp.AccessHandler(r.access),
	)
	if err != nil {
		fmt.Fprintf(opt.Stderr, "explorerepo: %v\n", err)
		return Result{Status: 128}
	}
	err = ip.Run(ctx, file)
	status := 0
	if err != nil {
		if s, ok := interp.IsExitStatus(err); ok {
			status = int(s)
		} else if lw.truncated.Load() || errors.Is(err, context.Canceled) {
			status = 0
		} else if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(opt.Stderr, "explorerepo: timed out")
			status = 124
		} else {
			fmt.Fprintf(opt.Stderr, "explorerepo: %v\n", err)
			status = 1
		}
	}
	return finish(opt, lw, status)
}

// ---- handlers: everything resolves through the vfs ----

type memFile struct {
	*bytes.Reader
}

func (memFile) Write([]byte) (int, error) { return 0, vfs.ErrReadOnly }
func (memFile) Close() error              { return nil }

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (nopWriteCloser) Close() error             { return nil }

type devNull struct{}

func (devNull) Read([]byte) (int, error)    { return 0, io.EOF }
func (devNull) Write(p []byte) (int, error) { return len(p), nil }
func (devNull) Close() error                { return nil }

func (r *runner) open(ctx context.Context, p string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	switch p {
	case "/dev/null":
		return devNull{}, nil
	case "/dev/stdout":
		return nopWriteCloser{interp.HandlerCtx(ctx).Stdout}, nil
	case "/dev/stderr":
		return nopWriteCloser{interp.HandlerCtx(ctx).Stderr}, nil
	}
	if strings.HasPrefix(p, "/dev/fd/") || strings.HasPrefix(p, "/proc/") {
		return nil, &os.PathError{Op: "sh:", Path: p, Err: errors.New("process substitution and fd paths are not supported")}
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC) != 0 {
		return nil, &os.PathError{Op: "sh:", Path: p, Err: vfs.ErrReadOnly}
	}
	n, err := r.opt.FS.Stat(ctx, p)
	if err != nil {
		return nil, &os.PathError{Op: "sh:", Path: p, Err: err}
	}
	data, err := r.opt.FS.Read(ctx, n)
	if err != nil {
		return nil, &os.PathError{Op: "sh:", Path: p, Err: err}
	}
	return memFile{bytes.NewReader(data)}, nil
}

func (r *runner) readDir(ctx context.Context, p string) ([]fs.DirEntry, error) {
	nodes, err := r.opt.FS.ReadDir(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]fs.DirEntry, len(nodes))
	for i, n := range nodes {
		out[i] = fs.FileInfoToDirEntry(r.opt.FS.FileInfo(ctx, n))
	}
	return out, nil
}

func (r *runner) stat(ctx context.Context, p string, follow bool) (fs.FileInfo, error) {
	var n *vfs.Node
	var err error
	if follow {
		n, err = r.opt.FS.Stat(ctx, p)
	} else {
		n, err = r.opt.FS.Lstat(ctx, p)
	}
	if err != nil {
		return nil, err
	}
	return r.opt.FS.FileInfo(ctx, n), nil
}

func (r *runner) access(ctx context.Context, p string, mode interp.AccessMode) error {
	n, err := r.opt.FS.Stat(ctx, p)
	if err != nil {
		return err
	}
	if mode&interp.AccessWrite != 0 {
		return vfs.ErrReadOnly
	}
	if n.IsGitlink() {
		return &vfs.SubmoduleError{Commit: n.OID}
	}
	return nil
}

// call intercepts "cd" so failures read like coreutils errors (and name
// submodules), the availability probes "type" and "command -v" so they see
// the builtins the shell actually provides, and "help", which the interpreter
// would otherwise answer with its own builtin list.
func (r *runner) call(ctx context.Context, args []string) ([]string, error) {
	if args[0] == "help" {
		text, ok := builtins.Help(args[1:])
		w := interp.HandlerCtx(ctx).Stdout
		io.WriteString(w, builtins.HelpStyled(r.color(w), args[1:], text, ok))
		if !ok {
			return []string{"false"}, nil
		}
		return []string{"true"}, nil
	}
	if args[0] == "type" || args[0] == "command" {
		hc := interp.HandlerCtx(ctx)
		if handled, status := probe(args, hc.Stdout, hc.Stderr); handled {
			if status == builtins.ExitOK {
				return []string{"true"}, nil
			}
			return []string{"false"}, nil
		}
		return args, nil
	}
	if args[0] != "cd" {
		return args, nil
	}
	var operands []string
	for _, a := range args[1:] {
		if a == "-L" || a == "-P" || a == "--" {
			continue
		}
		operands = append(operands, a)
	}
	if len(operands) != 1 || operands[0] == "-" {
		return args, nil
	}
	hc := interp.HandlerCtx(ctx)
	abs := operands[0]
	if !strings.HasPrefix(abs, "/") {
		abs = path.Join(hc.Dir, abs)
	}
	n, err := r.opt.FS.Stat(ctx, path.Clean(abs))
	switch {
	case err != nil:
		fmt.Fprintf(hc.Stderr, "cd: %s: %v\n", operands[0], err)
	case n.IsGitlink():
		fmt.Fprintf(hc.Stderr, "cd: %s: %v\n", operands[0], &vfs.SubmoduleError{Commit: n.OID})
	case !n.IsDir():
		fmt.Fprintf(hc.Stderr, "cd: %s: %v\n", operands[0], vfs.ErrNotDir)
	default:
		return args, nil
	}
	return []string{"false"}, nil
}
