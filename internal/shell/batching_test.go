package shell_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/autowert66/explorerepo/internal/testutil"
)

func blobFetches(log []string) int {
	n := 0
	for _, l := range log {
		if strings.HasPrefix(l, "fetch") && strings.Contains(l, `filter=""`) {
			n++
		}
	}
	return n
}

func TestColdAndWarmCat(t *testing.T) {
	f := testutil.NewFixture(t)
	cache := t.TempDir()
	r := runAt(t, f.Bare, cache, "HEAD", "cat a/b/c/d/d.txt")
	expect(t, "cold cat", r, "delta\n", 0)
	// ls-refs + commit + root tree + 4 directory trees + 1 blob
	if r.ct.Requests("ls-refs") != 1 || r.ct.Requests("fetch") != 7 || r.ct.Sessions() != 1 {
		t.Fatalf("cold: ls-refs=%d fetch=%d sessions=%d\n%v", r.ct.Requests("ls-refs"), r.ct.Requests("fetch"), r.ct.Sessions(), r.ct.Log())
	}
	// same command, warm object cache and fresh ref cache => no network at all
	r = runAt(t, f.Bare, cache, "HEAD", "cat a/b/c/d/d.txt")
	expect(t, "warm cat", r, "delta\n", 0)
	if r.ct.Sessions() != 0 || r.ct.Requests("fetch") != 0 || r.ct.Requests("ls-refs") != 0 {
		t.Fatalf("warm: sessions=%d fetch=%d ls-refs=%d", r.ct.Sessions(), r.ct.Requests("fetch"), r.ct.Requests("ls-refs"))
	}
}

func TestListingIsOneTreeFetch(t *testing.T) {
	f := testutil.NewFixture(t)
	r := runAt(t, f.Bare, "", "HEAD", "ls a/b")
	expect(t, "ls", r, "b.txt\nc\n", 0)
	// commit + root + a + a/b; children's trees (c) are not fetched
	if got := r.ct.Requests("fetch"); got != 4 {
		t.Fatalf("fetches = %d: %v", got, r.ct.Log())
	}
}

func TestRecursiveBatching(t *testing.T) {
	f := testutil.NewFixture(t)
	// find -maxdepth 2: one request per level (root, then all depth-1 dirs together)
	r := runAt(t, f.Bare, "", "HEAD", "find . -maxdepth 2 -type d | wc -l")
	expect(t, "find", r, "8\n", 0)
	if got := r.ct.Requests("fetch"); got != 3 {
		t.Fatalf("find -maxdepth 2: fetches = %d: %v", got, r.ct.Log())
	}
	// unbounded: one blob:none subtree fetch
	r = runAt(t, f.Bare, "", "HEAD", "find . -type f | wc -l")
	expect(t, "find all", r, "12\n", 0)
	if got := r.ct.Requests("fetch"); got != 2 { // commit + one subtree prefetch (which includes the root tree)
		t.Fatalf("find: fetches = %d: %v", got, r.ct.Log())
	}
	subtree := 0
	for _, l := range r.ct.Log() {
		if strings.Contains(l, "blob:none") {
			subtree++
		}
	}
	if subtree != 1 {
		t.Fatalf("expected exactly one blob:none prefetch, log: %v", r.ct.Log())
	}
}

func TestMultiFileCatSingleBlobFetch(t *testing.T) {
	f := testutil.NewFixture(t)
	r := runAt(t, f.Bare, "", "HEAD", "cat README.md a/a.txt empty.txt > /dev/null; head -1 README.md a/a.txt > /dev/null")
	if r.status != 0 {
		t.Fatalf("status %d: %s", r.status, r.err)
	}
	// README.md, a/a.txt and empty.txt are fetched together: one blob request in total
	if got := blobFetches(r.ct.Log()); got != 1 {
		t.Fatalf("blob fetches = %d: %v", got, r.ct.Log())
	}
}

func TestGrepFetchesBoundedByChunks(t *testing.T) {
	for _, n := range []int{50, 1500, 2100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			bare := testutil.NewWide(t, n)
			r := runAt(t, bare, "", "HEAD", "grep -rn needle w | wc -l")
			expect(t, "grep", r, fmt.Sprintf("%d\n", n), 0)
			// commit + root tree + one subtree prefetch + ceil(n/1000) blob requests
			want := 3 + (n+999)/1000
			if got := r.ct.Requests("fetch"); got != want {
				t.Fatalf("n=%d: fetches = %d, want %d: %v", n, got, want, r.ct.Log())
			}
			if r.ct.Sessions() != 1 {
				t.Fatalf("sessions = %d", r.ct.Sessions())
			}
		})
	}
}

func TestGrepEarlyExit(t *testing.T) {
	bare := testutil.NewWide(t, 2500)
	r := runAt(t, bare, "", "HEAD", "grep -rn needle w | head -1")
	expect(t, "grep|head", r, "w/f00000.txt:1:line needle 0\n", 0)
	if r.err != "" {
		t.Fatalf("stderr: %q", r.err)
	}
	if got := blobFetches(r.ct.Log()); got > 2 {
		t.Errorf("blob fetches = %d; closed pipe should stop the scan early", got)
	}
}

func TestPrefetchIsReusedAcrossCommands(t *testing.T) {
	f := testutil.NewFixture(t)
	r := runAt(t, f.Bare, "", "HEAD", "find . -type f | wc -l; tree -d | tail -1; ls -R a | wc -l; grep -rl needle . | wc -l")
	if r.status != 0 {
		t.Fatalf("status %d: %s", r.status, r.err)
	}
	subtree := 0
	for _, l := range r.ct.Log() {
		if strings.Contains(l, "blob:none") {
			subtree++
		}
	}
	if subtree != 1 {
		t.Fatalf("subtree prefetch ran %d times: %v", subtree, r.ct.Log())
	}
}
