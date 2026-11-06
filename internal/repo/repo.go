// Package repo is the lazy, caching view of a remote repository: it turns
// "I need this tree/blob" into the smallest batch of upload-pack requests.
package repo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/pack"
	"github.com/autowert66/explorerepo/internal/protocol"
	"github.com/autowert66/explorerepo/internal/store"
	"github.com/autowert66/explorerepo/internal/transport"
)

// MaxWantsPerFetch caps want lines in one request.
const MaxWantsPerFetch = 1000

// RefsTTL is how long a ref listing is trusted.
const RefsTTL = 10 * time.Minute

// BudgetError is returned when the per-invocation fetch budget is exhausted.
type BudgetError struct{ Limit int64 }

func (e *BudgetError) Error() string {
	return fmt.Sprintf("fetch budget exceeded (%d MiB); narrow the path or raise --max-fetch", e.Limit>>20)
}

type Config struct {
	Transport transport.Transport
	Store     *store.Store
	CacheKey  string
	Display   string
	MaxFetch  int64 // bytes of pack data per invocation (0 = 256 MiB)
	Refresh   bool  // ignore cached refs
	Logf      func(format string, args ...any)
}

type Info struct {
	Repo     string
	Rev      string
	Commit   object.OID
	Time     time.Time
	RootTree object.OID
}

type Repo struct {
	cfg     Config
	store   *store.Store
	fetched atomic.Int64
	max     int64

	cmu    sync.Mutex
	client *protocol.Client

	tmu   sync.Mutex
	trees map[object.OID][]object.Entry

	cmpMu  sync.Mutex
	cmpMem map[object.OID]bool

	info Info
}

func New(cfg Config) *Repo {
	max := cfg.MaxFetch
	if max <= 0 {
		max = 256 << 20
	}
	return &Repo{cfg: cfg, store: cfg.Store, max: max, trees: map[object.OID][]object.Entry{}, cmpMem: map[object.OID]bool{}}
}

func (r *Repo) logf(f string, a ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf(f, a...)
	}
}

func (r *Repo) Close() {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	if r.client != nil {
		r.client.Close()
		r.client = nil
	}
}

// FetchedBytes reports pack bytes received so far.
func (r *Repo) FetchedBytes() int64 { return r.fetched.Load() }

func (r *Repo) Info() Info { return r.info }

// withClient runs fn on the session, redialing once if the session broke.
func (r *Repo) withClient(ctx context.Context, fn func(*protocol.Client) error) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var c *protocol.Client
		c, err = r.session(ctx)
		if err != nil {
			return err
		}
		err = fn(c)
		if err == nil {
			return nil
		}
		var be *BudgetError
		if errors.Is(err, protocol.ErrBudget) || errors.As(err, &be) || ctx.Err() != nil || !c.Broken() {
			return err
		}
		r.logf("session broke (%v); redialing", err)
		r.cmu.Lock()
		if r.client == c {
			r.client.Close()
			r.client = nil
		}
		r.cmu.Unlock()
	}
	return err
}

func (r *Repo) session(ctx context.Context) (*protocol.Client, error) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	if r.client != nil && !r.client.Broken() {
		return r.client, nil
	}
	if r.client != nil {
		r.client.Close()
		r.client = nil
	}
	start := time.Now()
	c, err := protocol.Dial(ctx, r.cfg.Transport)
	if err != nil {
		return nil, err
	}
	c.Logf = r.cfg.Logf
	r.logf("connected in %v", time.Since(start).Round(time.Millisecond))
	r.client = c
	return c, nil
}

// ---- refs & revisions ----

func fromEntry(e *store.RefsEntry) map[string]protocol.Ref {
	m := map[string]protocol.Ref{}
	for _, x := range e.Refs {
		ref := protocol.Ref{Name: x.Name, SymrefTarget: x.Symref}
		ref.OID, _ = object.ParseOID(x.OID)
		if x.Peeled != "" {
			ref.Peeled, _ = object.ParseOID(x.Peeled)
		}
		m[x.Name] = ref
	}
	return m
}

// ListRefs performs a live ls-refs for the given prefixes.
func (r *Repo) ListRefs(ctx context.Context, prefixes []string) ([]protocol.Ref, error) {
	var refs []protocol.Ref
	err := r.withClient(ctx, func(c *protocol.Client) error {
		var err error
		refs, err = c.LsRefs(ctx, prefixes)
		return err
	})
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, err
}

func (r *Repo) defaultRefs(ctx context.Context, force bool) (map[string]protocol.Ref, bool, error) {
	if !force && !r.cfg.Refresh {
		if e, ok := r.store.LoadRefs(r.cfg.CacheKey, RefsTTL); ok {
			return fromEntry(e), true, nil
		}
	}
	refs, err := r.ListRefs(ctx, []string{"HEAD", "refs/heads/", "refs/tags/"})
	if err != nil {
		return nil, false, err
	}
	e := &store.RefsEntry{FetchedAt: time.Now()}
	m := map[string]protocol.Ref{}
	for _, x := range refs {
		m[x.Name] = x
		re := store.RefEntry{Name: x.Name, OID: x.OID.String(), Symref: x.SymrefTarget}
		if !x.Peeled.IsZero() {
			re.Peeled = x.Peeled.String()
		}
		e.Refs = append(e.Refs, re)
	}
	r.store.SaveRefs(r.cfg.CacheKey, e)
	return m, false, nil
}

func lookup(m map[string]protocol.Ref, rev string) (protocol.Ref, bool) {
	var cands []string
	switch {
	case rev == "" || rev == "HEAD":
		cands = []string{"HEAD"}
	case strings.HasPrefix(rev, "refs/"):
		cands = []string{rev}
	default:
		cands = []string{"refs/heads/" + rev, "refs/tags/" + rev}
	}
	for _, c := range cands {
		if ref, ok := m[c]; ok {
			return ref, true
		}
	}
	return protocol.Ref{}, false
}

// ResolveRev maps a rev (ref name, full OID, "" or HEAD) to an object id
// (a commit, or for annotated tags the peeled commit).
func (r *Repo) ResolveRev(ctx context.Context, rev string) (object.OID, error) {
	if len(rev) == 40 && object.IsHex(rev) {
		return object.ParseOID(strings.ToLower(rev))
	}
	m, cached, err := r.defaultRefs(ctx, false)
	if err != nil {
		return object.OID{}, err
	}
	ref, ok := lookup(m, rev)
	if !ok && cached {
		if m, _, err = r.defaultRefs(ctx, true); err != nil {
			return object.OID{}, err
		}
		ref, ok = lookup(m, rev)
	}
	if !ok && strings.HasPrefix(rev, "refs/") {
		refs, err := r.ListRefs(ctx, []string{rev})
		if err != nil {
			return object.OID{}, err
		}
		for _, x := range refs {
			if x.Name == rev {
				ref, ok = x, true
			}
		}
	}
	if !ok {
		if object.IsHex(rev) && len(rev) >= 4 {
			return object.OID{}, fmt.Errorf("cannot resolve abbreviated object id %q: use a full 40-character OID or a ref name", rev)
		}
		return object.OID{}, fmt.Errorf("unknown revision %q", rev)
	}
	if !ref.Peeled.IsZero() {
		return ref.Peeled, nil
	}
	return ref.OID, nil
}

// Open resolves rev and loads the commit, populating Info.
func (r *Repo) Open(ctx context.Context, rev string) error {
	oid, err := r.ResolveRev(ctx, rev)
	if err != nil {
		return err
	}
	commit, ci, err := r.peel(ctx, oid)
	if err != nil {
		return err
	}
	r.info = Info{Repo: r.cfg.Display, Rev: rev, Commit: commit, Time: ci.Time, RootTree: ci.Tree}
	return nil
}

// peel follows annotated tags until a commit is reached.
func (r *Repo) peel(ctx context.Context, oid object.OID) (object.OID, *object.CommitInfo, error) {
	for i := 0; i < 10; i++ {
		t, data, err := r.object(ctx, oid, "tree:0", 1)
		if err != nil {
			return oid, nil, err
		}
		switch t {
		case object.Commit:
			ci, err := object.ParseCommit(data)
			return oid, ci, err
		case object.Tag:
			target, _, err := object.ParseTag(data)
			if err != nil {
				return oid, nil, err
			}
			oid = target
		default:
			return oid, nil, fmt.Errorf("%s is a %s, not a commit", oid, t)
		}
	}
	return oid, nil, errors.New("tag chain too deep")
}

// Commit returns the parsed commit for oid (fetching it, with deepen 1, if needed).
func (r *Repo) Commit(ctx context.Context, oid object.OID) (*object.CommitInfo, error) {
	t, data, err := r.object(ctx, oid, "tree:0", 1)
	if err != nil {
		return nil, err
	}
	if t != object.Commit {
		return nil, fmt.Errorf("%s is a %s, not a commit", oid, t)
	}
	return object.ParseCommit(data)
}

// ---- object fetching ----

func (r *Repo) object(ctx context.Context, oid object.OID, filter string, deepen int) (object.Type, []byte, error) {
	if t, d, ok := r.store.Get(oid); ok {
		return t, d, nil
	}
	if err := r.fetch(ctx, []object.OID{oid}, filter, deepen); err != nil {
		return 0, nil, err
	}
	t, d, ok := r.store.Get(oid)
	if !ok {
		return 0, nil, fmt.Errorf("server did not return object %s", oid)
	}
	return t, d, nil
}

func (r *Repo) fetch(ctx context.Context, wants []object.OID, filter string, deepen int) error {
	for len(wants) > 0 {
		n := len(wants)
		if n > MaxWantsPerFetch {
			n = MaxWantsPerFetch
		}
		chunk := wants[:n]
		wants = wants[n:]
		if err := r.fetchChunk(ctx, chunk, filter, deepen); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) fetchChunk(ctx context.Context, wants []object.OID, filter string, deepen int) error {
	start := time.Now()
	var res protocol.FetchResult
	err := r.withClient(ctx, func(c *protocol.Client) error {
		remaining := r.max - r.fetched.Load()
		if remaining <= 0 {
			return &BudgetError{r.max}
		}
		var err error
		res, err = c.Fetch(ctx, protocol.FetchReq{
			Wants: wants, Filter: filter, Deepen: deepen, MaxBytes: remaining,
			Base: r.store.Get,
		}, func(o pack.Raw) error { return r.store.Put(o.OID, o.Type, o.Data) })
		r.fetched.Add(res.PackBytes)
		if errors.Is(err, protocol.ErrBudget) {
			return &BudgetError{r.max}
		}
		return err
	})
	r.logf("fetch wants=%d filter=%q deepen=%d -> %d objects, %d bytes, %v",
		len(wants), filter, deepen, res.Objects, res.PackBytes, time.Since(start).Round(time.Millisecond))
	if err != nil {
		return err
	}
	for _, w := range wants {
		if !r.store.Has(w) {
			return fmt.Errorf("server did not return object %s", w)
		}
	}
	return nil
}

// ---- trees ----

func (r *Repo) parsedTree(oid object.OID) ([]object.Entry, bool, error) {
	r.tmu.Lock()
	if e, ok := r.trees[oid]; ok {
		r.tmu.Unlock()
		return e, true, nil
	}
	r.tmu.Unlock()
	t, data, ok := r.store.Get(oid)
	if !ok {
		return nil, false, nil
	}
	if t != object.Tree {
		return nil, false, fmt.Errorf("%s is a %s, not a tree", oid, t)
	}
	entries, err := object.ParseTree(data)
	if err != nil {
		return nil, false, fmt.Errorf("tree %s: %w", oid, err)
	}
	r.tmu.Lock()
	r.trees[oid] = entries
	r.tmu.Unlock()
	return entries, true, nil
}

// Tree returns the entries of a tree object, fetching just that tree if needed.
func (r *Repo) Tree(ctx context.Context, oid object.OID) ([]object.Entry, error) {
	if e, ok, err := r.parsedTree(oid); ok || err != nil {
		return e, err
	}
	if err := r.fetch(ctx, []object.OID{oid}, "tree:0", 0); err != nil {
		return nil, err
	}
	e, ok, err := r.parsedTree(oid)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("server did not return tree %s", oid)
	}
	return e, nil
}

// HasTree reports whether the tree is cached.
func (r *Repo) HasTree(oid object.OID) bool { return r.store.Has(oid) }

// Trees fetches all missing trees in as few requests as possible.
func (r *Repo) Trees(ctx context.Context, oids []object.OID) error {
	var missing []object.OID
	seen := map[object.OID]bool{}
	for _, o := range oids {
		if !seen[o] && !r.store.Has(o) {
			seen[o] = true
			missing = append(missing, o)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return r.fetch(ctx, missing, "tree:0", 0)
}

// SubtreeCached reports whether every tree below oid is already cached.
func (r *Repo) SubtreeCached(oid object.OID) bool {
	r.cmpMu.Lock()
	if r.cmpMem[oid] {
		r.cmpMu.Unlock()
		return true
	}
	r.cmpMu.Unlock()
	ok := r.subtreeCached(oid)
	if ok {
		r.cmpMu.Lock()
		r.cmpMem[oid] = true
		r.cmpMu.Unlock()
	}
	return ok
}

func (r *Repo) subtreeCached(oid object.OID) bool {
	if r.store.IsComplete(oid) {
		return true
	}
	entries, ok, err := r.parsedTree(oid)
	if !ok || err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && !r.SubtreeCached(e.OID) {
			return false
		}
	}
	return true
}

// PrefetchSubtrees makes every tree below oid available with one request.
func (r *Repo) PrefetchSubtrees(ctx context.Context, oid object.OID) error {
	if r.SubtreeCached(oid) {
		return nil
	}
	if err := r.fetch(ctx, []object.OID{oid}, "blob:none", 0); err != nil {
		return err
	}
	r.store.MarkComplete(oid)
	r.cmpMu.Lock()
	r.cmpMem[oid] = true
	r.cmpMu.Unlock()
	return nil
}

// ---- blobs ----

func (r *Repo) Blob(ctx context.Context, oid object.OID) ([]byte, error) {
	t, d, err := r.object(ctx, oid, "", 0)
	if err != nil {
		return nil, err
	}
	if t != object.Blob {
		return nil, fmt.Errorf("%s is a %s, not a blob", oid, t)
	}
	return d, nil
}

// HasBlob reports whether the blob is cached.
func (r *Repo) HasBlob(oid object.OID) bool { return r.store.Has(oid) }

// Blobs fetches all missing blobs in chunks of MaxWantsPerFetch.
func (r *Repo) Blobs(ctx context.Context, oids []object.OID) error {
	var missing []object.OID
	seen := map[object.OID]bool{}
	for _, o := range oids {
		if !seen[o] && !r.store.Has(o) {
			seen[o] = true
			missing = append(missing, o)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return r.fetch(ctx, missing, "", 0)
}

// Sizes returns known blob sizes: from the cache, then via object-info if the
// server supports it. Unknown sizes are simply absent from the result.
func (r *Repo) Sizes(ctx context.Context, oids []object.OID) map[object.OID]int64 {
	out := map[object.OID]int64{}
	var unknown []object.OID
	for _, o := range oids {
		if n, ok := r.store.Size(o); ok {
			out[o] = n
		} else {
			unknown = append(unknown, o)
		}
	}
	if len(unknown) == 0 {
		return out
	}
	c, err := r.session(ctx)
	if err != nil || !c.Caps.ObjectInfo {
		return out
	}
	for len(unknown) > 0 {
		n := len(unknown)
		if n > MaxWantsPerFetch {
			n = MaxWantsPerFetch
		}
		m, err := c.ObjectInfo(ctx, unknown[:n])
		if err != nil {
			break
		}
		for k, v := range m {
			out[k] = v
		}
		unknown = unknown[n:]
	}
	return out
}

// Log fetches up to n commits ending at oid (no trees or blobs).
func (r *Repo) Log(ctx context.Context, oid object.OID, n int) ([]LogEntry, error) {
	if err := r.fetch(ctx, []object.OID{oid}, "tree:0", n); err != nil {
		return nil, err
	}
	var out []LogEntry
	seen := map[object.OID]bool{}
	queue := []object.OID{oid}
	for len(queue) > 0 && len(out) < n {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		t, data, ok := r.store.Get(cur)
		if !ok || t != object.Commit {
			continue
		}
		ci, err := object.ParseCommit(data)
		if err != nil {
			return nil, err
		}
		out = append(out, LogEntry{OID: cur, Commit: ci})
		queue = append(queue, ci.Parents...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Commit.Time.After(out[j].Commit.Time) })
	return out, nil
}

type LogEntry struct {
	OID    object.OID
	Commit *object.CommitInfo
}

// CachedSize returns the size of a cached blob, or -1.
func (r *Repo) CachedSize(oid object.OID) int64 {
	if n, ok := r.store.Size(oid); ok {
		return n
	}
	return -1
}

// CommitTree resolves rev to a commit and returns its parsed form.
func (r *Repo) CommitTree(ctx context.Context, rev string) (object.OID, *object.CommitInfo, error) {
	oid, err := r.ResolveRev(ctx, rev)
	if err != nil {
		return oid, nil, err
	}
	return r.peel(ctx, oid)
}
