package builtins

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

func init() { register("find", "search for files (-name -path -type -maxdepth ...)", findCmd) }

type fctx struct {
	e        WalkEntry
	prune    bool
	acted    bool
	env      *Env
	ctx      context.Context
	execBufs map[*execExpr][]string
}

type fexpr interface{ eval(c *fctx) bool }

type (
	andExpr  struct{ l, r fexpr }
	orExpr   struct{ l, r fexpr }
	notExpr  struct{ x fexpr }
	nameExpr struct {
		pat  string
		fold bool
	}
	pathExpr struct {
		pat  string
		fold bool
	}
	regexExpr struct{ re *regexp.Regexp }
	typeExpr  struct{ t byte }
	printExpr struct{ nul bool }
	pruneExpr struct{}
	boolExpr  bool
	noopExpr  struct{} // -maxdepth / -mindepth (handled globally)
	execExpr  struct {
		argv  []string
		batch bool
	}
)

func (x andExpr) eval(c *fctx) bool { return x.l.eval(c) && x.r.eval(c) }
func (x orExpr) eval(c *fctx) bool  { return x.l.eval(c) || x.r.eval(c) }
func (x notExpr) eval(c *fctx) bool { return !x.x.eval(c) }
func (x nameExpr) eval(c *fctx) bool {
	base := c.e.Display
	if i := strings.LastIndex(strings.TrimRight(base, "/"), "/"); i >= 0 && base != "/" {
		base = strings.TrimRight(base, "/")[i+1:]
	}
	return GlobMatch(x.pat, base, x.fold)
}
func (x pathExpr) eval(c *fctx) bool  { return GlobMatch(x.pat, c.e.Display, x.fold) }
func (x regexExpr) eval(c *fctx) bool { return x.re.MatchString(c.e.Display) }
func (x typeExpr) eval(c *fctx) bool {
	n := c.e.Node
	switch x.t {
	case 'f':
		return n.IsRegular()
	case 'd':
		return n.IsDir() || n.IsGitlink()
	case 'l':
		return n.IsSymlink()
	}
	return false
}
func (x printExpr) eval(c *fctx) bool {
	c.acted = true
	c.env.Stdout.WriteString(c.e.Display)
	if x.nul {
		c.env.Stdout.WriteString("\x00")
	} else {
		c.env.Stdout.WriteString("\n")
	}
	return true
}
func (pruneExpr) eval(c *fctx) bool { c.prune = true; return true }
func (x boolExpr) eval(*fctx) bool  { return bool(x) }
func (noopExpr) eval(*fctx) bool    { return true }
func (x *execExpr) eval(c *fctx) bool {
	c.acted = true
	if x.batch {
		c.execBufs[x] = append(c.execBufs[x], c.e.Display)
		return true
	}
	argv := make([]string, len(x.argv))
	for i, a := range x.argv {
		argv[i] = strings.ReplaceAll(a, "{}", c.e.Display)
	}
	c.env.Stdout.Flush()
	return c.env.Run(c.ctx, argv, strings.NewReader(""), c.env.Stdout) == 0
}

type findParser struct {
	toks     []string
	pos      int
	err      error
	hasAct   bool
	maxDepth int
	minDepth int
	execs    []*execExpr
}

func (p *findParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}
func (p *findParser) more() bool { return p.pos < len(p.toks) }
func (p *findParser) fail(format string, a ...any) fexpr {
	if p.err == nil {
		p.err = fmt.Errorf(format, a...)
	}
	return boolExpr(false)
}

func (p *findParser) parseOr() fexpr {
	l := p.parseAnd()
	for p.more() && (p.peek() == "-o" || p.peek() == "-or") {
		p.pos++
		l = orExpr{l, p.parseAnd()}
	}
	return l
}

func (p *findParser) parseAnd() fexpr {
	l := p.parseUnary()
	for p.more() && p.peek() != "-o" && p.peek() != "-or" && p.peek() != ")" {
		if p.peek() == "-a" || p.peek() == "-and" {
			p.pos++
		}
		l = andExpr{l, p.parseUnary()}
	}
	return l
}

func (p *findParser) arg(flag string) string {
	if p.pos >= len(p.toks) {
		p.fail("missing argument to '%s'", flag)
		return ""
	}
	v := p.toks[p.pos]
	p.pos++
	return v
}

func (p *findParser) parseUnary() fexpr {
	if !p.more() {
		return p.fail("expression expected")
	}
	t := p.toks[p.pos]
	p.pos++
	switch t {
	case "!", "-not":
		return notExpr{p.parseUnary()}
	case "(":
		x := p.parseOr()
		if p.peek() != ")" {
			return p.fail("missing closing ')'")
		}
		p.pos++
		return x
	case "-name":
		return nameExpr{p.arg(t), false}
	case "-iname":
		return nameExpr{p.arg(t), true}
	case "-path", "-wholename":
		return pathExpr{p.arg(t), false}
	case "-ipath", "-iwholename":
		return pathExpr{p.arg(t), true}
	case "-regex", "-iregex":
		pat := p.arg(t)
		if t == "-iregex" {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile("^(?:" + pat + ")$")
		if err != nil {
			return p.fail("invalid regex %q", pat)
		}
		return regexExpr{re}
	case "-type":
		v := p.arg(t)
		if len(v) != 1 || !strings.Contains("fdlbcps", v) {
			return p.fail("Unknown argument to -type: %s", v)
		}
		return typeExpr{v[0]}
	case "-maxdepth", "-mindepth":
		n, err := strconv.Atoi(p.arg(t))
		if err != nil || n < 0 {
			return p.fail("invalid argument to %s", t)
		}
		if t == "-maxdepth" {
			p.maxDepth = n
		} else {
			p.minDepth = n
		}
		return noopExpr{}
	case "-print":
		p.hasAct = true
		return printExpr{}
	case "-print0":
		p.hasAct = true
		return printExpr{nul: true}
	case "-prune":
		return pruneExpr{}
	case "-true":
		return boolExpr(true)
	case "-false":
		return boolExpr(false)
	case "-exec", "-execdir":
		p.hasAct = true
		x := &execExpr{}
		for {
			if !p.more() {
				return p.fail("missing argument to '-exec'")
			}
			a := p.toks[p.pos]
			p.pos++
			if a == ";" {
				break
			}
			if a == "+" && len(x.argv) > 0 && x.argv[len(x.argv)-1] == "{}" {
				x.batch = true
				x.argv = x.argv[:len(x.argv)-1]
				break
			}
			x.argv = append(x.argv, a)
		}
		if len(x.argv) == 0 {
			return p.fail("missing command for -exec")
		}
		p.execs = append(p.execs, x)
		return x
	}
	return p.fail("unsupported predicate '%s' (supported: -name -iname -path -ipath -regex -type f|d|l -maxdepth -mindepth -prune -print -print0 -exec ! -o -a ( ))", t)
}

func findCmd(ctx context.Context, env *Env, args []string) int {
	var starts []string
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") && args[i] != "!" && args[i] != "(" {
		starts = append(starts, args[i])
		i++
	}
	if len(starts) == 0 {
		starts = []string{"."}
	}
	p := &findParser{toks: args[i:], maxDepth: -1}
	var expr fexpr = boolExpr(true)
	if len(p.toks) > 0 {
		expr = p.parseOr()
		if p.err == nil && p.more() {
			p.fail("unexpected '%s'", p.peek())
		}
	}
	if p.err != nil {
		return env.usage("find", "%v", p.err)
	}
	if !p.hasAct {
		expr = andExpr{expr, printExpr{}}
	}
	c := &fctx{env: env, ctx: ctx, execBufs: map[*execExpr][]string{}}
	status := ExitOK
	for _, s := range starts {
		n, err := env.FS.Stat(ctx, env.Abs(s))
		if err != nil {
			fmt.Fprintf(env.Stderr, "find: '%s': %s\n", s, errText(err))
			status = ExitFail
			continue
		}
		err = env.Walk(ctx, n, s, WalkOpts{MaxDepth: p.maxDepth}, func(e WalkEntry) error {
			if e.Depth < p.minDepth {
				return nil
			}
			c.e, c.prune = e, false
			expr.eval(c)
			if c.prune && e.Node.IsDir() {
				return ErrSkipDir
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil || env.Stdout.Err() != nil {
				return ExitSigPipe
			}
			env.PathErr("find", s, err)
			status = ExitFail
		}
	}
	for _, x := range p.execs {
		if x.batch && len(c.execBufs[x]) > 0 {
			argv := append(append([]string{}, x.argv...), c.execBufs[x]...)
			env.Stdout.Flush()
			if env.Run(ctx, argv, strings.NewReader(""), env.Stdout) != 0 {
				status = ExitFail
			}
		}
	}
	return status
}

func errText(err error) string { return err.Error() }

var _ = path.Base
