package repo_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"explorerepo/internal/object"
	"explorerepo/internal/repo"
	"explorerepo/internal/repotest"
	"explorerepo/internal/store"
	"explorerepo/internal/testutil"
	"explorerepo/internal/transport"
)

var ctx = context.Background()

func TestResolveRev(t *testing.T) {
	f := testutil.NewFixture(t)
	r, _ := repotest.Open(t, f.Bare, "", "HEAD")
	cases := map[string]object.OID{
		"":                   f.Commit2,
		"HEAD":               f.Commit2,
		"main":               f.Commit2,
		"feature":            f.Feature,
		"v1-light":           f.Commit1,
		"v1":                 f.Commit1, // annotated tag is peeled
		"refs/heads/feature": f.Feature,
		f.Commit1.String():   f.Commit1,
	}
	for rev, want := range cases {
		got, err := r.ResolveRev(ctx, rev)
		if err != nil || got != want {
			t.Errorf("%q: got %v err %v, want %v", rev, got, err, want)
		}
	}
	if _, err := r.ResolveRev(ctx, f.Commit1.String()[:8]); err == nil || !strings.Contains(err.Error(), "full") {
		t.Errorf("abbreviated oid: err = %v", err)
	}
	if _, err := r.ResolveRev(ctx, "nope"); err == nil {
		t.Error("unknown rev should fail")
	}
}

func TestOpenAnnotatedTagObjectOID(t *testing.T) {
	f := testutil.NewFixture(t)
	r, _ := repotest.Open(t, f.Bare, "", f.TagObj.String())
	if r.Info().Commit != f.Commit1 {
		t.Fatalf("commit = %v, want %v", r.Info().Commit, f.Commit1)
	}
}

func TestColdTreeWalkBatching(t *testing.T) {
	f := testutil.NewFixture(t)
	dir := t.TempDir()
	r, ct := repotest.Open(t, f.Bare, dir, "HEAD")
	// Open: 1 ls-refs + 1 commit fetch
	if ct.Requests("ls-refs") != 1 || ct.Requests("fetch") != 1 {
		t.Fatalf("open: ls-refs=%d fetch=%d (%v)", ct.Requests("ls-refs"), ct.Requests("fetch"), ct.Log())
	}
	root := r.Info().RootTree
	walk := func(r *repo.Repo, tree object.OID) {
		for _, name := range []string{"a", "b", "c", "d"} {
			es, err := r.Tree(ctx, tree)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range es {
				if e.Name == name {
					tree = e.OID
				}
			}
		}
		es, err := r.Tree(ctx, tree)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if e.Name == "d.txt" {
				b, err := r.Blob(ctx, e.OID)
				if err != nil || string(b) != "delta\n" {
					t.Fatalf("blob: %q %v", b, err)
				}
			}
		}
	}
	walk(r, root)
	// root + a + b + c + d trees, 1 blob, plus the commit => 7 fetches total
	if got := ct.Requests("fetch"); got != 7 {
		t.Fatalf("cold fetches = %d, want 7: %v", got, ct.Log())
	}
	// warm: new process (new Repo), same disk cache, ref cache still fresh => no network at all
	r2, ct2 := repotest.Open(t, f.Bare, dir, "HEAD")
	walk(r2, r2.Info().RootTree)
	if ct2.Sessions() != 0 {
		t.Fatalf("warm run opened %d sessions: %v", ct2.Sessions(), ct2.Log())
	}
}

func TestPrefetchSubtreesOneRequest(t *testing.T) {
	f := testutil.NewFixture(t)
	r, ct := repotest.Open(t, f.Bare, "", "HEAD")
	before := ct.Requests("fetch")
	if err := r.PrefetchSubtrees(ctx, r.Info().RootTree); err != nil {
		t.Fatal(err)
	}
	if got := ct.Requests("fetch") - before; got != 1 {
		t.Fatalf("prefetch used %d fetches", got)
	}
	if !r.SubtreeCached(r.Info().RootTree) {
		t.Fatal("subtree should be cached")
	}
	// idempotent
	r.PrefetchSubtrees(ctx, r.Info().RootTree)
	if got := ct.Requests("fetch") - before; got != 1 {
		t.Fatalf("second prefetch hit network (%d)", got)
	}
}

func TestBlobsChunking(t *testing.T) {
	f := testutil.NewFixture(t)
	r, ct := repotest.Open(t, f.Bare, "", "HEAD")
	r.PrefetchSubtrees(ctx, r.Info().RootTree)
	var oids []object.OID
	var collect func(o object.OID)
	collect = func(o object.OID) {
		es, _ := r.Tree(ctx, o)
		for _, e := range es {
			if e.IsDir() {
				collect(e.OID)
			} else if e.IsRegular() || e.IsSymlink() {
				oids = append(oids, e.OID)
			}
		}
	}
	collect(r.Info().RootTree)
	before := ct.Requests("fetch")
	if err := r.Blobs(ctx, oids); err != nil {
		t.Fatal(err)
	}
	if got := ct.Requests("fetch") - before; got != 1 {
		t.Fatalf("blob batch used %d fetches for %d blobs", got, len(oids))
	}
	for _, o := range oids {
		if !r.HasBlob(o) {
			t.Fatalf("missing %s", o)
		}
	}
}

func TestBudget(t *testing.T) {
	f := testutil.NewFixture(t)
	st, _ := store.Open("", 0)
	ct := transport.NewCounting(transport.Local{Path: f.Bare})
	r := repo.New(repo.Config{Transport: ct, Store: st, CacheKey: "b", MaxFetch: 100 << 10})
	defer r.Close()
	if err := r.Open(ctx, "HEAD"); err != nil {
		t.Fatal(err)
	}
	r.PrefetchSubtrees(ctx, r.Info().RootTree)
	es, _ := r.Tree(ctx, r.Info().RootTree)
	var dataTree object.OID
	for _, e := range es {
		if e.Name == "data" {
			dataTree = e.OID
		}
	}
	des, _ := r.Tree(ctx, dataTree)
	var big object.OID
	for _, e := range des {
		if e.Name == "big.txt" {
			big = e.OID
		}
	}
	_, err := r.Blob(ctx, big) // 3 MiB > 100 KiB budget
	var be *repo.BudgetError
	if !errors.As(err, &be) || !strings.Contains(err.Error(), "--max-fetch") {
		t.Fatalf("err = %v", err)
	}
}

func TestLog(t *testing.T) {
	f := testutil.NewFixture(t)
	r, _ := repotest.Open(t, f.Bare, "", "HEAD")
	l, err := r.Log(ctx, f.Commit2, 5)
	if err != nil || len(l) != 2 || l[0].OID != f.Commit2 || l[1].OID != f.Commit1 {
		t.Fatalf("log: %v %v", l, err)
	}
}

// severing wraps a transport and remembers the connections it opened.
type severing struct {
	inner transport.Transport
	conns []transport.Conn
}

func (s *severing) Open(ctx context.Context) (transport.Conn, error) {
	c, err := s.inner.Open(ctx)
	if err == nil {
		s.conns = append(s.conns, c)
	}
	return c, err
}

func TestRedialsOnceWhenSessionBreaks(t *testing.T) {
	f := testutil.NewFixture(t)
	st, _ := store.Open("", 0)
	sv := &severing{inner: transport.Local{Path: f.Bare}}
	ct := transport.NewCounting(sv)
	r := repo.New(repo.Config{Transport: ct, Store: st, CacheKey: "redial"})
	defer r.Close()
	if err := r.Open(ctx, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if ct.Sessions() != 1 {
		t.Fatalf("sessions = %d", ct.Sessions())
	}
	sv.conns[0].Close() // the server side goes away between requests
	es, err := r.Tree(ctx, r.Info().RootTree)
	if err != nil || len(es) == 0 {
		t.Fatalf("Tree after broken session: %v (%d entries)", err, len(es))
	}
	if ct.Sessions() != 2 {
		t.Fatalf("expected one transparent redial, sessions = %d", ct.Sessions())
	}
	// and it keeps working on the new session without further redials
	if _, err := r.Tree(ctx, r.Info().RootTree); err != nil {
		t.Fatal(err)
	}
	r.PrefetchSubtrees(ctx, r.Info().RootTree)
	if ct.Sessions() != 2 {
		t.Fatalf("sessions = %d", ct.Sessions())
	}
}
