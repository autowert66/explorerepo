package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/autowert66/explorerepo/internal/color"
	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/protocol"
	"github.com/autowert66/explorerepo/internal/repo"
	"github.com/autowert66/explorerepo/internal/repospec"
	"github.com/autowert66/explorerepo/internal/store"
	"github.com/autowert66/explorerepo/internal/transport"
)

// checkBudget bounds the pack bytes one transport's check may fetch; a
// subtree larger than this is reported as not checked rather than failed.
const checkBudget = 32 << 20

type namedTransport struct {
	name, url string
	tr        transport.Transport
}

// checkCmd tries each transport on its own, without the cache or the ssh->HTTPS
// fallback, and reports every step explorerepo needs from the server: connect
// with protocol v2 and filter support, list refs, fetch the commit, one tree, a
// subtree in one request, and a blob.
func checkCmd(ctx context.Context, spec *repospec.Spec, rev string, trs []namedTransport, timeout time.Duration, w io.Writer, s color.Styler) int {
	fmt.Fprintln(w, s.Bold("explorerepo --check "+spec.Display))
	var ok, failed []string
	for _, t := range trs {
		fmt.Fprintf(w, "\n%s  %s\n", s.Bold(t.name), t.url)
		tctx, cancel := context.WithTimeout(ctx, timeout)
		if checkTransport(tctx, spec, rev, t.tr, w, s) {
			ok = append(ok, t.name)
		} else {
			failed = append(failed, t.name)
		}
		cancel()
	}
	fmt.Fprintln(w)
	switch {
	case len(ok) == 0:
		fmt.Fprintf(w, "%s %s\n", s.Bold("verdict:"), s.Red("explorerepo cannot read "+spec.Display+" (see the FAIL lines above)"))
		return exitSetup
	case ok[0] == "https":
		fmt.Fprintf(w, "%s %s", s.Bold("verdict:"), s.Green("works over https (public repositories only)"))
		if len(failed) > 0 {
			fmt.Fprintf(w, "; ssh failed, so private repositories on %s need a working ssh key", spec.Host)
		}
		fmt.Fprintln(w)
	default:
		fmt.Fprintf(w, "%s %s\n", s.Bold("verdict:"), s.Green("works over "+strings.Join(ok, " and ")))
	}
	return 0
}

func checkTransport(ctx context.Context, spec *repospec.Spec, rev string, tr transport.Transport, w io.Writer, s color.Styler) bool {
	step := func(name string, fn func() (string, error)) bool {
		start := time.Now()
		msg, err := fn()
		d := time.Since(start).Round(time.Millisecond)
		if err != nil {
			fmt.Fprintf(w, "  %s %-8s %v %s\n", s.Err("FAIL"), name, err, s.Dim(fmt.Sprintf("(%v)", d)))
			return false
		}
		fmt.Fprintf(w, "  %s   %-8s %s %s\n", s.Green("ok"), name, msg, s.Dim(fmt.Sprintf("(%v)", d)))
		return true
	}
	if !step("connect", func() (string, error) {
		c, err := protocol.Dial(ctx, tr)
		if err != nil {
			return "", err
		}
		defer c.Close()
		agent := c.Caps.Raw["agent"]
		if agent == "" {
			agent = "unknown"
		}
		return "protocol v2, filter supported, agent " + agent, nil
	}) {
		return false
	}

	st, err := store.Open("", 0)
	if err != nil {
		fmt.Fprintf(w, "  %s %v\n", s.Err("FAIL"), err)
		return false
	}
	r := repo.New(repo.Config{Transport: tr, Store: st, CacheKey: spec.CacheKey(), Display: spec.Display, MaxFetch: checkBudget})
	defer r.Close()

	name := rev
	if name == "" {
		name = "HEAD"
	}
	var oid object.OID
	if !step("refs", func() (string, error) {
		oid, err = r.ResolveRev(ctx, rev)
		return fmt.Sprintf("%s -> %s", name, oid), err
	}) {
		return false
	}
	var root object.OID
	if !step("commit", func() (string, error) {
		_, ci, err := r.CommitTree(ctx, rev)
		if err != nil {
			return "", err
		}
		root = ci.Tree
		return "fetched with deepen 1, filter tree:0", nil
	}) {
		return false
	}
	var entries []object.Entry
	if !step("tree", func() (string, error) {
		entries, err = r.Tree(ctx, root)
		return fmt.Sprintf("root tree (%d entries), filter %s", len(entries), r.TreeFilter()), err
	}) {
		return false
	}
	var dir, file *object.Entry
	for i := range entries {
		e := &entries[i]
		switch {
		case dir == nil && e.IsDir():
			dir = e
		case file == nil && e.Mode&0o170000 == 0o100000:
			file = e
		}
	}
	if dir == nil {
		fmt.Fprintln(w, s.Dim("  --   subtree  no directory at the root to try"))
	} else if !step("subtree", func() (string, error) {
		before := r.FetchedBytes()
		err := r.PrefetchSubtrees(ctx, dir.OID)
		var be *repo.BudgetError
		if errors.As(err, &be) {
			return fmt.Sprintf("%s/ is over %d MiB, not checked", dir.Name, checkBudget>>20), nil
		}
		return fmt.Sprintf("%s/ in one request, filter blob:none, %d pack bytes", dir.Name, r.FetchedBytes()-before), err
	}) {
		return false
	}
	if file == nil {
		fmt.Fprintln(w, s.Dim("  --   blob     no file at the root to try"))
		return true
	}
	return step("blob", func() (string, error) {
		b, err := r.Blob(ctx, file.OID)
		return fmt.Sprintf("%s, %d bytes", file.Name, len(b)), err
	})
}
