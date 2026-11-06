package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/testutil"
)

func TestPutGetRoundTripAndGitCompat(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, 1<<20)
	data := []byte("hello world\n")
	oid := object.Hash(object.Blob, data)
	if err := s.Put(oid, object.Blob, data); err != nil {
		t.Fatal(err)
	}
	// fresh store (empty LRU) must read from disk
	s2, _ := Open(dir, 1<<20)
	ty, got, ok := s2.Get(oid)
	if !ok || ty != object.Blob || string(got) != string(data) {
		t.Fatalf("got %v %q %v", ty, got, ok)
	}
	if n, ok := s2.Size(oid); !ok || n != int64(len(data)) {
		t.Fatalf("size %d %v", n, ok)
	}
	// the cache is a valid git object directory
	out := runCatFile(t, filepath.Join(dir, "objects"), oid.String())
	if out != "hello world" {
		t.Fatalf("git cat-file: %q", out)
	}
}

func runCatFile(t *testing.T, objdir, oid string) string {
	t.Helper()
	os.Setenv("GIT_OBJECT_DIRECTORY", objdir)
	defer os.Unsetenv("GIT_OBJECT_DIRECTORY")
	repo := t.TempDir()
	testutil.Git(t, repo, nil, "init", "-q")
	return testutil.Git(t, repo, nil, "cat-file", "-p", oid)
}

func TestLRUEviction(t *testing.T) {
	s, _ := Open("", 100)
	for i := 0; i < 10; i++ {
		d := make([]byte, 20)
		d[0] = byte(i)
		s.Put(object.Hash(object.Blob, d), object.Blob, d)
	}
	if s.size > 100 {
		t.Fatalf("size %d exceeds cap", s.size)
	}
	first := make([]byte, 20)
	if s.Has(object.Hash(object.Blob, first)) {
		t.Error("oldest entry should be evicted from a memory-only store")
	}
}

func TestRefsTTL(t *testing.T) {
	s, _ := Open(t.TempDir(), 0)
	s.SaveRefs("k", &RefsEntry{FetchedAt: time.Now(), Refs: []RefEntry{{Name: "HEAD"}}})
	if _, ok := s.LoadRefs("k", time.Minute); !ok {
		t.Error("fresh entry should load")
	}
	s.SaveRefs("k", &RefsEntry{FetchedAt: time.Now().Add(-time.Hour)})
	if _, ok := s.LoadRefs("k", time.Minute); ok {
		t.Error("stale entry should not load")
	}
}

func TestCompleteMarker(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, 0)
	oid := object.Hash(object.Tree, nil)
	if s.IsComplete(oid) {
		t.Fatal("not complete yet")
	}
	s.MarkComplete(oid)
	s2, _ := Open(dir, 0)
	if !s2.IsComplete(oid) {
		t.Fatal("marker should persist")
	}
}
