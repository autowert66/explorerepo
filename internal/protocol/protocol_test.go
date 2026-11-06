package protocol

import (
	"context"
	"testing"

	"explorerepo/internal/object"
	"explorerepo/internal/pack"
	"explorerepo/internal/testutil"
	"explorerepo/internal/transport"
)

func dial(t *testing.T, f *testutil.Fixture) *Client {
	t.Helper()
	c, err := Dial(context.Background(), transport.Local{Path: f.Bare})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func fetch(t *testing.T, c *Client, req FetchReq) map[object.OID]object.Type {
	t.Helper()
	got := map[object.OID]object.Type{}
	_, err := c.Fetch(context.Background(), req, func(r pack.Raw) error { got[r.OID] = r.Type; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCapsAndLsRefs(t *testing.T) {
	f := testutil.NewFixture(t)
	c := dial(t, f)
	if !c.Caps.Fetch["filter"] {
		t.Fatal("filter not advertised")
	}
	refs, err := c.LsRefs(context.Background(), []string{"HEAD", "refs/heads/", "refs/tags/"})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Ref{}
	for _, r := range refs {
		by[r.Name] = r
	}
	if by["HEAD"].SymrefTarget != "refs/heads/main" || by["HEAD"].OID != f.Commit2 {
		t.Errorf("HEAD = %+v", by["HEAD"])
	}
	if by["refs/tags/v1"].OID != f.TagObj || by["refs/tags/v1"].Peeled != f.Commit1 {
		t.Errorf("v1 = %+v", by["refs/tags/v1"])
	}
	if by["refs/tags/v1-light"].OID != f.Commit1 || !by["refs/tags/v1-light"].Peeled.IsZero() {
		t.Errorf("v1-light = %+v", by["refs/tags/v1-light"])
	}
	if by["refs/heads/feature"].OID != f.Feature {
		t.Errorf("feature = %+v", by["refs/heads/feature"])
	}
}

func TestFetchShapes(t *testing.T) {
	f := testutil.NewFixture(t)
	c := dial(t, f) // one session for all requests (probe 6)

	// commit + deepen 1 + tree:0 => the commit only (tree:1 is rejected by GitHub)
	var commitData []byte
	got := map[object.OID]object.Type{}
	_, err := c.Fetch(context.Background(), FetchReq{Wants: []object.OID{f.Commit2}, Deepen: 1, Filter: "tree:0"},
		func(r pack.Raw) error { got[r.OID] = r.Type; commitData = r.Data; return nil })
	if err != nil || len(got) != 1 || got[f.Commit2] != object.Commit {
		t.Fatalf("commit fetch: %v %v", got, err)
	}
	ci, err := object.ParseCommit(commitData)
	if err != nil {
		t.Fatal(err)
	}
	root := ci.Tree

	// a wanted tree + tree:0 => just that tree (tree:1 would also return its children)
	got = fetch(t, c, FetchReq{Wants: []object.OID{root}, Filter: "tree:0"})
	if len(got) != 1 {
		t.Fatalf("tree fetch returned %d objects", len(got))
	}

	// tree + blob:none => whole tree structure, no blobs
	got = fetch(t, c, FetchReq{Wants: []object.OID{root}, Filter: "blob:none"})
	trees := 0
	for _, ty := range got {
		if ty == object.Blob {
			t.Fatal("blob returned with blob:none")
		}
		trees++
	}
	if trees < 8 {
		t.Fatalf("expected many trees, got %d", trees)
	}

	// a non-tip blob alone
	blob := object.Hash(object.Blob, []byte("alpha\n")) // a/a.txt in commit 1
	got = fetch(t, c, FetchReq{Wants: []object.OID{blob}})
	if len(got) != 1 || got[blob] != object.Blob {
		t.Fatalf("blob fetch: %v", got)
	}
}
