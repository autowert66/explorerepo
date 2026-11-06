// Command explorerepo lets an agent explore a remote git repository without
// cloning it: Git protocol v2 over ssh, lazy filtered fetches, and an embedded
// read-only shell over the commit's virtual tree.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"explorerepo/internal/builtins"
	"explorerepo/internal/object"
	"explorerepo/internal/pack"
	"explorerepo/internal/protocol"
	"explorerepo/internal/repo"
	"explorerepo/internal/repospec"
	"explorerepo/internal/shell"
	"explorerepo/internal/store"
	"explorerepo/internal/transport"
	"explorerepo/internal/vfs"
)

const exitSetup = 128

// byteSize is a flag.Value accepting K/M/G suffixes.
type byteSize int64

func (b *byteSize) String() string { return strconv.FormatInt(int64(*b), 10) }
func (b *byteSize) Set(s string) error {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	for suffix, m := range map[string]int64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30} {
		if strings.HasSuffix(s, suffix) {
			mult, s = m, strings.TrimSuffix(s, suffix)
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("invalid size")
	}
	*b = byteSize(n * mult)
	return nil
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("explorerepo", flag.ContinueOnError)
	fl.SetOutput(stderr)
	fl.Usage = func() { fmt.Fprint(stderr, usage) }
	var (
		repoArg   = fl.String("repo", os.Getenv("EXPLOREREPO_REPO"), "repository")
		rev       = fl.String("rev", "", "revision")
		script    = fl.String("c", "", "script")
		refresh   = fl.Bool("refresh", false, "ignore cached refs")
		noCache   = fl.Bool("no-cache", false, "memory-only cache")
		noMux     = fl.Bool("no-mux", false, "disable ssh ControlMaster")
		maxOutput = byteSize(200 << 10)
		maxFetch  = byteSize(256 << 20)
		maxFiles  = fl.Int("max-files", 20000, "max files per content scan")
		timeout   = fl.Duration("timeout", 60*time.Second, "overall timeout")
	)
	fl.StringVar(rev, "hash", "", "alias for --rev")
	fl.Var(&maxOutput, "max-output", "max stdout bytes")
	fl.Var(&maxFetch, "max-fetch", "max pack bytes fetched")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitSetup
	}
	rest := fl.Args()

	if len(rest) > 0 && rest[0] == "help" {
		fmt.Fprint(stdout, builtins.Help(rest[1:]))
		return 0
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "explorerepo: "+format+"\n", a...)
		return exitSetup
	}
	if *repoArg == "" {
		fail("--repo is required (or set EXPLOREREPO_REPO)")
		fmt.Fprint(stderr, usage)
		return exitSetup
	}
	spec, err := repospec.Parse(*repoArg)
	if err != nil {
		return fail("%v", err)
	}

	var tr transport.Transport
	if spec.Local {
		tr = transport.Local{Path: spec.Path}
	} else {
		tr = transport.SSH{User: spec.User, Host: spec.Host, Port: spec.Port, Path: spec.Path, NoMux: *noMux}
	}
	dir := ""
	if !*noCache {
		if dir, err = store.DefaultDir(); err != nil {
			return fail("cache dir: %v", err)
		}
	}
	st, err := store.Open(dir, 0)
	if err != nil {
		return fail("opening cache: %v", err)
	}
	var logf func(string, ...any)
	if os.Getenv("EXPLOREREPO_DEBUG") != "" {
		logf = func(f string, a ...any) { fmt.Fprintf(stderr, "[debug] "+f+"\n", a...) }
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	if len(rest) > 0 && rest[0] == "debug" {
		return debugCmd(ctx, tr, rest[1:], stdout, stderr)
	}

	r := repo.New(repo.Config{
		Transport: tr, Store: st, CacheKey: spec.CacheKey(), Display: spec.Display,
		MaxFetch: int64(maxFetch), Refresh: *refresh, Logf: logf,
	})
	defer r.Close()
	if err := r.Open(ctx, *rev); err != nil {
		return fail("%v", err)
	}
	fsys := vfs.New(r)
	fsys.Warn = func(m string) { fmt.Fprintf(stderr, "explorerepo: warning: %s\n", m) }

	name := spec.Display
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	opt := shell.Options{
		Repo: r, FS: fsys, Stdin: stdin, Stdout: stdout, Stderr: stderr, MaxOutput: int64(maxOutput),
		Cfg: &builtins.Config{MaxFiles: *maxFiles, RepoName: name, Rev: *rev},
	}

	var res shell.Result
	switch {
	case *script != "":
		res = shell.Run(ctx, opt, *script)
	case len(rest) > 0:
		res = shell.RunCommand(ctx, opt, rest)
	default:
		b, err := io.ReadAll(stdin)
		if err != nil {
			return fail("reading script: %v", err)
		}
		opt.Stdin = strings.NewReader("")
		res = shell.Run(ctx, opt, string(b))
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && res.Status == 0 && !res.Truncated {
		// a command swallowed the cancellation; make the timeout visible
		fmt.Fprintln(stderr, "explorerepo: timed out")
		return 124
	}
	return res.Status
}

func debugCmd(ctx context.Context, tr transport.Transport, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: explorerepo --repo SPEC debug lsrefs [PREFIX...] | fetch OID [--filter F] [--deepen N]")
		return exitSetup
	}
	c, err := protocol.Dial(ctx, tr)
	if err != nil {
		fmt.Fprintf(stderr, "explorerepo: %v\n", err)
		return exitSetup
	}
	defer c.Close()
	switch args[0] {
	case "caps":
		for k, v := range c.Caps.Raw {
			fmt.Fprintf(stdout, "%s=%s\n", k, v)
		}
	case "lsrefs":
		refs, err := c.LsRefs(ctx, args[1:])
		if err != nil {
			fmt.Fprintf(stderr, "explorerepo: %v\n", err)
			return exitSetup
		}
		for _, r := range refs {
			fmt.Fprintf(stdout, "%s %s", r.OID, r.Name)
			if r.SymrefTarget != "" {
				fmt.Fprintf(stdout, " symref-target:%s", r.SymrefTarget)
			}
			if !r.Peeled.IsZero() {
				fmt.Fprintf(stdout, " peeled:%s", r.Peeled)
			}
			fmt.Fprintln(stdout)
		}
	case "fetch":
		fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
		filter := fs.String("filter", "", "filter spec")
		deepen := fs.Int("deepen", 0, "deepen")
		var oids []string
		for len(args) > 1 {
			args = args[1:]
			if strings.HasPrefix(args[0], "-") {
				if err := fs.Parse(args); err != nil {
					return exitSetup
				}
				args = append([]string{"fetch"}, fs.Args()...)
				continue
			}
			oids = append(oids, args[0])
			args = append([]string{"fetch"}, args[1:]...)
		}
		var wants []object.OID
		for _, s := range oids {
			o, err := object.ParseOID(s)
			if err != nil {
				fmt.Fprintf(stderr, "explorerepo: %v\n", err)
				return exitSetup
			}
			wants = append(wants, o)
		}
		res, err := c.Fetch(ctx, protocol.FetchReq{Wants: wants, Filter: *filter, Deepen: *deepen}, func(o pack.Raw) error {
			fmt.Fprintf(stdout, "%s %s %d\n", o.OID, o.Type, len(o.Data))
			return nil
		})
		if err != nil {
			fmt.Fprintf(stderr, "explorerepo: %v\n", err)
			return exitSetup
		}
		fmt.Fprintf(stdout, "# %d objects, %d pack bytes\n", res.Objects, res.PackBytes)
	default:
		fmt.Fprintf(stderr, "explorerepo: unknown debug command %q\n", args[0])
		return exitSetup
	}
	return 0
}

const usage = `usage: explorerepo --repo SPEC [--rev REV] [options] -c 'SCRIPT'
       explorerepo --repo SPEC [--rev REV] [options] CMD [ARGS...]
       explorerepo --repo SPEC [--rev REV] [options] < script.sh
       explorerepo help [CMD]

options: --rev (alias --hash)  --refresh  --no-cache  --no-mux
         --max-output BYTES (200K)  --max-fetch BYTES (256M)
         --max-files N (20000)  --timeout DUR (60s)
env:     EXPLOREREPO_REPO  EXPLOREREPO_CACHE  EXPLOREREPO_DEBUG=1
`
