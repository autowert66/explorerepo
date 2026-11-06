// Package store is a content-addressed object cache: an in-memory LRU in front
// of git-compatible loose objects on disk, plus a small TTL'd ref cache.
package store

import (
	"bytes"
	"compress/zlib"
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/autowert66/explorerepo/internal/object"
)

type memEntry struct {
	oid  object.OID
	typ  object.Type
	data []byte
}

type Store struct {
	dir string // "" = memory only

	mu       sync.Mutex
	lru      *list.List
	items    map[object.OID]*list.Element
	size     int64
	max      int64
	complete map[object.OID]bool
}

// DefaultDir returns the cache directory honoring $EXPLOREREPO_CACHE.
func DefaultDir() (string, error) {
	if d := os.Getenv("EXPLOREREPO_CACHE"); d != "" {
		return d, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "explorerepo"), nil
}

// Open creates a store rooted at dir (memory-only if dir is empty).
func Open(dir string, memMax int64) (*Store, error) {
	if memMax <= 0 {
		memMax = 64 << 20
	}
	s := &Store{dir: dir, lru: list.New(), items: map[object.OID]*list.Element{}, max: memMax, complete: map[object.OID]bool{}}
	if dir != "" {
		if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) objPath(oid object.OID) string {
	h := oid.String()
	return filepath.Join(s.dir, "objects", h[:2], h[2:])
}

func (s *Store) remember(oid object.OID, t object.Type, data []byte) {
	if int64(len(data)) > s.max/4 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[oid]; ok {
		s.lru.MoveToFront(el)
		return
	}
	s.items[oid] = s.lru.PushFront(&memEntry{oid, t, data})
	s.size += int64(len(data))
	for s.size > s.max {
		el := s.lru.Back()
		e := el.Value.(*memEntry)
		s.lru.Remove(el)
		delete(s.items, e.oid)
		s.size -= int64(len(e.data))
	}
}

func (s *Store) fromMem(oid object.OID) (*memEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[oid]; ok {
		s.lru.MoveToFront(el)
		return el.Value.(*memEntry), true
	}
	return nil, false
}

// Get returns the object if cached. The returned slice must not be modified.
func (s *Store) Get(oid object.OID) (object.Type, []byte, bool) {
	if e, ok := s.fromMem(oid); ok {
		return e.typ, e.data, true
	}
	if s.dir == "" {
		return 0, nil, false
	}
	f, err := os.Open(s.objPath(oid))
	if err != nil {
		return 0, nil, false
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return 0, nil, false
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return 0, nil, false
	}
	t, data, ok := splitLoose(raw)
	if !ok {
		return 0, nil, false
	}
	s.remember(oid, t, data)
	return t, data, true
}

func splitLoose(raw []byte) (object.Type, []byte, bool) {
	nul := bytes.IndexByte(raw, 0)
	if nul < 0 {
		return 0, nil, false
	}
	ts, ls, ok := bytes.Cut(raw[:nul], []byte{' '})
	if !ok {
		return 0, nil, false
	}
	t, err := object.ParseType(string(ts))
	n, err2 := strconv.Atoi(string(ls))
	if err != nil || err2 != nil || n != len(raw)-nul-1 {
		return 0, nil, false
	}
	return t, raw[nul+1:], true
}

// Has reports whether the object is cached, without reading it.
func (s *Store) Has(oid object.OID) bool {
	s.mu.Lock()
	_, ok := s.items[oid]
	s.mu.Unlock()
	if ok {
		return true
	}
	if s.dir == "" {
		return false
	}
	_, err := os.Stat(s.objPath(oid))
	return err == nil
}

// Size returns the size of a cached object without inflating all of it.
func (s *Store) Size(oid object.OID) (int64, bool) {
	if e, ok := s.fromMem(oid); ok {
		return int64(len(e.data)), true
	}
	if s.dir == "" {
		return 0, false
	}
	f, err := os.Open(s.objPath(oid))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return 0, false
	}
	var hdr [40]byte
	n, _ := io.ReadAtLeast(zr, hdr[:], 1)
	nul := bytes.IndexByte(hdr[:n], 0)
	if nul < 0 {
		return 0, false
	}
	_, ls, ok := bytes.Cut(hdr[:nul], []byte{' '})
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(string(ls), 10, 64)
	return v, err == nil
}

// Put stores an object (caller guarantees oid == hash(type,data)).
func (s *Store) Put(oid object.OID, t object.Type, data []byte) error {
	s.remember(oid, t, data)
	if s.dir == "" {
		return nil
	}
	p := s.objPath(oid)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-")
	if err != nil {
		return err
	}
	zw, _ := zlib.NewWriterLevel(tmp, zlib.BestSpeed)
	zw.Write(object.Header(t, len(data)))
	zw.Write(data)
	zw.Close()
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (s *Store) completePath(oid object.OID) string {
	h := oid.String()
	return filepath.Join(s.dir, "complete", h[:2], h[2:])
}

// MarkComplete records that every tree below oid is cached.
func (s *Store) MarkComplete(oid object.OID) {
	s.mu.Lock()
	s.complete[oid] = true
	s.mu.Unlock()
	if s.dir == "" {
		return
	}
	p := s.completePath(oid)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, nil, 0o644)
}

func (s *Store) IsComplete(oid object.OID) bool {
	s.mu.Lock()
	ok := s.complete[oid]
	s.mu.Unlock()
	if ok {
		return true
	}
	if s.dir == "" {
		return false
	}
	if _, err := os.Stat(s.completePath(oid)); err == nil {
		s.mu.Lock()
		s.complete[oid] = true
		s.mu.Unlock()
		return true
	}
	return false
}

// RefsEntry is a cached ls-refs result.
type RefsEntry struct {
	FetchedAt time.Time  `json:"fetched_at"`
	Refs      []RefEntry `json:"refs"`
}

type RefEntry struct {
	Name   string `json:"name"`
	OID    string `json:"oid"`
	Peeled string `json:"peeled,omitempty"`
	Symref string `json:"symref,omitempty"`
}

func (s *Store) refsPath(key string) string { return filepath.Join(s.dir, "refs", key+".json") }

func (s *Store) LoadRefs(key string, ttl time.Duration) (*RefsEntry, bool) {
	if s.dir == "" {
		return nil, false
	}
	b, err := os.ReadFile(s.refsPath(key))
	if err != nil {
		return nil, false
	}
	var e RefsEntry
	if json.Unmarshal(b, &e) != nil || time.Since(e.FetchedAt) > ttl {
		return nil, false
	}
	return &e, true
}

func (s *Store) SaveRefs(key string, e *RefsEntry) error {
	if s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.refsPath(key)), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(e)
	tmp := s.refsPath(key) + fmt.Sprintf(".tmp%d", os.Getpid())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.refsPath(key))
}

var ErrNotFound = errors.New("object not found")
