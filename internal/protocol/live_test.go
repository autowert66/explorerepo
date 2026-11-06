package protocol

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"explorerepo/internal/object"
	"explorerepo/internal/pack"
	"explorerepo/internal/transport"
)

// TestLiveProbe records filter behaviour against github.com. EXPLOREREPO_LIVE=1 to run.
func TestLiveProbe(t *testing.T) {
	if os.Getenv("EXPLOREREPO_LIVE") == "" {
		t.Skip("set EXPLOREREPO_LIVE=1")
	}
	ctx := context.Background()
	start := time.Now()
	ct := transport.NewCounting(transport.SSH{User: "git", Host: "github.com", Path: "octocat/Hello-World.git"})
	c, err := Dial(ctx, ct)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Printf("dial %v; caps=%v object-info=%v\n", time.Since(start), c.Caps.Raw, c.Caps.ObjectInfo)
	refs, err := c.LsRefs(ctx, []string{"HEAD", "refs/heads/master"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("refs: %+v\n", refs)
	try := func(label string, req FetchReq) map[object.OID]object.Type {
		cc, err := Dial(ctx, ct)
		if err != nil {
			t.Fatal(err)
		}
		defer cc.Close()
		n := map[object.Type]int{}
		all := map[object.OID]object.Type{}
		_, err = cc.Fetch(ctx, req, func(r pack.Raw) error { n[r.Type]++; all[r.OID] = r.Type; return nil })
		fmt.Printf("%-34s -> %v err=%v\n", label, n, err)
		return all
	}
	head := refs[0].OID
	try("commit deepen1 tree:1", FetchReq{Wants: []object.OID{head}, Deepen: 1, Filter: "tree:1"})
	all := try("commit deepen1 tree:0", FetchReq{Wants: []object.OID{head}, Deepen: 1, Filter: "tree:0"})
	_ = all
	all = try("commit deepen1 blob:none", FetchReq{Wants: []object.OID{head}, Deepen: 1, Filter: "blob:none"})
	var root object.OID
	for o, ty := range all {
		if ty == object.Tree {
			root = o
		}
	}
	for _, f := range []string{"tree:0", "blob:none", "blob:limit=1k", ""} {
		try("tree "+f, FetchReq{Wants: []object.OID{root}, Filter: f})
	}
	// multi-fetch on a single session + blob by oid
	cc, _ := Dial(ctx, ct)
	defer cc.Close()
	for i := 0; i < 3; i++ {
		n := 0
		_, err := cc.Fetch(ctx, FetchReq{Wants: []object.OID{root}, Filter: "tree:0"}, func(pack.Raw) error { n++; return nil })
		fmt.Printf("same-session fetch #%d -> %d objs err=%v\n", i+1, n, err)
	}
	fmt.Printf("sessions=%d requests: fetch=%d ls-refs=%d\n", ct.Sessions(), ct.Requests("fetch"), ct.Requests("ls-refs"))
	// second invocation through the mux
	start = time.Now()
	c2, err := Dial(ctx, ct)
	if err != nil {
		t.Fatal(err)
	}
	c2.Close()
	fmt.Printf("second dial (mux) %v\n", time.Since(start))
}
