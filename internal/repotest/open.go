package repotest

import (
	"context"
	"testing"

	"github.com/autowert66/explorerepo/internal/repo"
	"github.com/autowert66/explorerepo/internal/store"
	"github.com/autowert66/explorerepo/internal/transport"
)

// Open returns a Repo over the fixture with HEAD (or rev) resolved, using a
// counting local transport. cacheDir "" means a memory-only store.
func Open(t testing.TB, bare, cacheDir, rev string) (*repo.Repo, *transport.Counting) {
	t.Helper()
	st, err := store.Open(cacheDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	ct := transport.NewCounting(transport.Local{Path: bare})
	r := repo.New(repo.Config{Transport: ct, Store: st, CacheKey: "test-" + bare, Display: "fixture"})
	t.Cleanup(r.Close)
	if err := r.Open(context.Background(), rev); err != nil {
		t.Fatal(err)
	}
	return r, ct
}
