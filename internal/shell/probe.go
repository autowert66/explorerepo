package shell

import (
	"fmt"
	"io"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/autowert66/explorerepo/internal/builtins"
)

// cmdKind classifies a name for the availability probes (type, command -v,
// which). The shell provides no external files, so every command it knows is
// reported as a builtin and the probes print the bare name where bash would
// print a path. Agents use these probes to decide whether a tool exists, so a
// false "not found" makes them fall back to a worse one.
type cmdKind int

const (
	kindMissing cmdKind = iota
	kindBuiltin
	kindKeyword
)

// resolve classifies name as a command the shell provides or a shell keyword.
func resolve(name string) cmdKind {
	if _, ok := builtins.Lookup(name); ok {
		return kindBuiltin
	}
	if interp.IsBuiltin(name) {
		return kindBuiltin
	}
	if syntax.IsKeyword(name) {
		return kindKeyword
	}
	return kindMissing
}

// probe implements the availability probes agents reach for: type,
// command -v/-V, and which. It returns handled=false when args[0] is not one of
// them, or when the interpreter should answer instead (a plain `command NAME`,
// or a `type` of names the interpreter may know as functions/aliases), so the
// caller can fall through.
func probe(args []string, stdout, stderr io.Writer) (bool, int) {
	switch args[0] {
	case "type":
		return probeType(args[1:], stdout, stderr)
	case "command":
		return probeCommand(args[1:], stdout, stderr)
	case "which":
		return probeWhich(args[1:], stdout, stderr)
	}
	return false, 0
}

func probeType(args []string, stdout, stderr io.Writer) (bool, int) {
	p, err := builtins.ParseOpts(args, []builtins.Opt{
		{Short: 'a'}, {Short: 'f'}, {Short: 'p'}, {Short: 'P'}, {Short: 't'},
	}, "")
	if err != nil {
		fmt.Fprintf(stderr, "type: %v\n", err)
		return true, builtins.ExitUsage
	}
	mode := ""
	switch {
	case p.Has("t"):
		mode = "t"
	case p.Has("p") || p.Has("P"):
		mode = "p"
	case p.Has("a"):
		mode = "a"
	}

	// If nothing resolves, let the interpreter answer: it still knows about
	// user-defined functions and aliases, which we do not.
	known := false
	for _, n := range p.Args {
		if resolve(n) != kindMissing {
			known = true
			break
		}
	}
	if !known {
		return false, 0
	}

	status := builtins.ExitOK
	for _, n := range p.Args {
		switch resolve(n) {
		case kindKeyword:
			switch mode {
			case "t":
				fmt.Fprintln(stdout, "keyword")
			case "p":
				status = builtins.ExitFail
			default:
				fmt.Fprintf(stdout, "%s is a shell keyword\n", n)
			}
		case kindBuiltin:
			switch mode {
			case "t":
				fmt.Fprintln(stdout, "builtin")
			case "p":
				// No path exists; the bare name is the runnable location.
				fmt.Fprintln(stdout, n)
			default:
				fmt.Fprintf(stdout, "%s is a shell builtin\n", n)
			}
		default:
			if mode == "" || mode == "a" {
				fmt.Fprintf(stderr, "type: %s: not found\n", n)
			}
			status = builtins.ExitFail
		}
	}
	return true, status
}

func probeCommand(args []string, stdout, stderr io.Writer) (bool, int) {
	p, err := builtins.ParseOpts(args, []builtins.Opt{
		{Short: 'v'}, {Short: 'V'}, {Short: 'p'},
	}, "")
	if err != nil {
		fmt.Fprintf(stderr, "command: %v\n", err)
		return true, builtins.ExitUsage
	}
	verbose := p.Has("V")
	if !verbose && !p.Has("v") {
		return false, 0 // plain `command NAME ...`: run it as usual
	}
	status := builtins.ExitOK
	for _, n := range p.Args {
		kind := resolve(n)
		if kind == kindMissing {
			if verbose {
				fmt.Fprintf(stderr, "command: %s: not found\n", n)
			}
			status = builtins.ExitFail
			continue
		}
		if !verbose {
			fmt.Fprintln(stdout, n)
			continue
		}
		if kind == kindKeyword {
			fmt.Fprintf(stdout, "%s is a shell keyword\n", n)
		} else {
			fmt.Fprintf(stdout, "%s is a shell builtin\n", n)
		}
	}
	return true, status
}

func probeWhich(args []string, stdout, stderr io.Writer) (bool, int) {
	p, err := builtins.ParseOpts(args, []builtins.Opt{
		{Short: 'a'}, {Short: 's'},
	}, "")
	if err != nil {
		fmt.Fprintf(stderr, "which: %v\n", err)
		return true, builtins.ExitUsage
	}
	if len(p.Args) == 0 {
		fmt.Fprintln(stderr, "usage: which [-as] program ...")
		return true, builtins.ExitFail
	}
	silent := p.Has("s")
	status := builtins.ExitOK
	for _, n := range p.Args {
		if resolve(n) != kindBuiltin {
			status = builtins.ExitFail
			continue
		}
		if !silent {
			fmt.Fprintln(stdout, n)
		}
	}
	return true, status
}
