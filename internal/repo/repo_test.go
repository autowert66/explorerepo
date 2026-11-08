package repo_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/repo"
	"github.com/autowert66/explorerepo/internal/repotest"
	"github.com/autowert66/explorerepo/internal/store"
	"github.com/autowert66/explorerepo/internal/testutil"
	"github.com/autowert66/explorerepo/internal/transport"
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

func TestLogKeepsChildBeforeParentUnderClockSkew(t *testing.T) {
	bare, child, parent := testutil.NewClockSkew(t)
	r, _ := repotest.Open(t, bare, "", "HEAD")
	l, err := r.Log(ctx, child, 2)
	if err != nil || len(l) != 2 || l[0].OID != child || l[1].OID != parent {
		t.Fatalf("log order: %v %v (child %s parent %s)", l, err, child, parent)
	}
}

func TestLogEqualDatesKeepGitInsertionOrder(t *testing.T) {
	bare, want := testutil.NewMergeOrder(t)
	r, _ := repotest.Open(t, bare, "", "HEAD")
	l, err := r.Log(ctx, want[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != len(want) {
		t.Fatalf("log length %d, want %d: %v", len(l), len(want), l)
	}
	for i := range want {
		if l[i].OID != want[i] {
			t.Fatalf("log[%d] = %s, want %s (full %v)", i, l[i].OID, want[i], l)
		}
	}
}

func TestLogFetchesEnoughForSmallCount(t *testing.T) {
	bare, tip, want := testutil.NewSkewChain(t, 20)
	r, _ := repotest.Open(t, bare, "", "HEAD")
	l, err := r.Log(ctx, tip, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 5 {
		t.Fatalf("log length %d, want 5: %v", len(l), l)
	}
	for i := range l {
		if l[i].OID != want[i] {
			t.Fatalf("log[%d] = %s, want %s", i, l[i].OID, want[i])
		}
	}
}

func TestResolveRevPrefersTagOverBranch(t *testing.T) {
	f := testutil.NewFixture(t)
	// git rev-parse prefers refs/tags/<name> over refs/heads/<name>.
	testutil.Git(t, f.Bare, nil, "update-ref", "refs/heads/v1", f.Commit2.String())
	r, _ := repotest.Open(t, f.Bare, "", "HEAD")
	got, err := r.ResolveRev(ctx, "v1")
	if err != nil || got != f.Commit1 {
		t.Fatalf("v1 = %s, %v; want tag's commit %s", got, err, f.Commit1)
	}
}

func TestUnbornRepoSaysThereAreNoCommits(t *testing.T) {
	bare := testutil.NewUnborn(t)
	st, err := store.Open("", 0)
	if err != nil {
		t.Fatal(err)
	}
	r := repo.New(repo.Config{Transport: transport.Local{Path: bare}, Store: st, CacheKey: "unborn", Display: "unborn"})
	defer r.Close()
	err = r.Open(ctx, "")
	if err == nil || (!strings.Contains(err.Error(), "no commits") && !strings.Contains(err.Error(), "unborn")) {
		t.Fatalf("unborn open: %v", err)
	}
}

func TestPeelLongTagChainByOID(t *testing.T) {
	bare, commit, deepest := testutil.NewTagChain(t, 12)
	r, _ := repotest.Open(t, bare, "", "HEAD")
	got, _, err := r.CommitTree(ctx, deepest.String())
	if err != nil || got != commit {
		t.Fatalf("tag chain = %s, %v; want %s", got, err, commit)
	}
}

func TestPeelTagChainAtTheLimit(t *testing.T) {
	// 100 tag objects is the documented maximum; the commit behind them must
	// still be reachable.
	bare, commit, deepest := testutil.NewTagChain(t, 100)
	r, _ := repotest.Open(t, bare, "", "HEAD")
	got, _, err := r.CommitTree(ctx, deepest.String())
	if err != nil || got != commit {
		t.Fatalf("tag chain = %s, %v; want %s", got, err, commit)
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
