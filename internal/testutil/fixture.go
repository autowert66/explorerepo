// Package testutil builds throwaway git repositories for offline tests.
package testutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/autowert66/git-explorer/internal/object"
)

// Fixture is a bare repository served by the local git-upload-pack.
type Fixture struct {
	Bare    string // bare repo path (use as --repo)
	Work    string // working tree it was cloned from
	Commit1 object.OID
	Commit2 object.OID // tip of main
	Feature object.OID // tip of branch "feature"
	TagObj  object.OID // annotated tag object v1 -> Commit1
	Gitlink object.OID // commit id recorded in the submodule entry
}

func env() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000",
	)
}

// Git runs the git CLI in dir and returns trimmed stdout.
func Git(t testing.TB, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func write(t testing.TB, root, name string, data []byte, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
}

// BigText returns deterministic ~3 MiB of line-oriented text; variant changes one line.
func BigText(variant int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < 3<<20; i++ {
		if i == 1000 && variant > 0 {
			fmt.Fprintf(&b, "line %06d CHANGED in variant %d\n", i, variant)
			continue
		}
		fmt.Fprintf(&b, "line %06d the quick brown fox %x\n", i, uint32(i)*2654435761)
	}
	return b.Bytes()
}

func NewFixture(t testing.TB) *Fixture {
	t.Helper()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	os.MkdirAll(work, 0o755)
	Git(t, work, nil, "init", "-q", "-b", "main")

	write(t, work, "README.md", []byte("# Hello\nsecond line\nthird line\n"), 0o644)
	write(t, work, "a/a.txt", []byte("alpha\n"), 0o644)
	write(t, work, "a/b/b.txt", []byte("bravo\n"), 0o644)
	write(t, work, "a/b/c/c.txt", []byte("charlie\n"), 0o644)
	write(t, work, "a/b/c/d/d.txt", []byte("delta\n"), 0o644)
	write(t, work, "a/b/c/d/needle.txt", []byte("hay\nneedle in a haystack\nhay\n"), 0o644)
	write(t, work, "dir with space/file with space.txt", []byte("spaced\n"), 0o644)
	write(t, work, "ünï/çödé.txt", []byte("unicode needle\n"), 0o644)
	write(t, work, "bin/tool.sh", []byte("#!/bin/sh\necho hi\n"), 0o755)
	write(t, work, "data/bin.dat", append([]byte("BIN\x00\x01\x02needle\n"), make([]byte, 100)...), 0o644)
	write(t, work, "data/big.txt", BigText(0), 0o644)
	write(t, work, "empty.txt", nil, 0o644)
	for name, target := range map[string]string{
		"link-to-readme": "README.md",
		"a/link-up":      "../README.md",
		"a/link-dir":     "b/c",
		"escape":         "../../etc/passwd",
		"abs":            "/etc/passwd",
		"loop1":          "loop2",
		"loop2":          "loop1",
	} {
		p := filepath.Join(work, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	f := &Fixture{Work: work}
	gl := "0123456789abcdef0123456789abcdef01234567"
	f.Gitlink, _ = object.ParseOID(gl)
	Git(t, work, nil, "add", "-A")
	Git(t, work, nil, "update-index", "--add", "--cacheinfo", "160000,"+gl+",sub")
	Git(t, work, nil, "commit", "-q", "-m", "first")
	f.Commit1, _ = object.ParseOID(Git(t, work, nil, "rev-parse", "HEAD"))
	Git(t, work, nil, "tag", "v1-light")
	Git(t, work, nil, "tag", "-a", "-m", "release v1", "v1")
	f.TagObj, _ = object.ParseOID(Git(t, work, nil, "rev-parse", "v1"))

	Git(t, work, nil, "checkout", "-q", "-b", "feature")
	write(t, work, "feature.txt", []byte("feature branch\n"), 0o644)
	Git(t, work, nil, "add", "-A")
	Git(t, work, nil, "commit", "-q", "-m", "feature")
	f.Feature, _ = object.ParseOID(Git(t, work, nil, "rev-parse", "HEAD"))
	Git(t, work, nil, "checkout", "-q", "main")

	write(t, work, "data/big.txt", BigText(1), 0o644)
	write(t, work, "a/a.txt", []byte("alpha v2\n"), 0o644)
	Git(t, work, nil, "commit", "-q", "-am", "second")
	f.Commit2, _ = object.ParseOID(Git(t, work, nil, "rev-parse", "HEAD"))

	f.Bare = filepath.Join(tmp, "bare.git")
	Git(t, tmp, nil, "clone", "-q", "--bare", work, f.Bare)
	Git(t, f.Bare, nil, "config", "uploadpack.allowFilter", "true")
	Git(t, f.Bare, nil, "config", "uploadpack.allowAnySHA1InWant", "true")
	Git(t, f.Bare, nil, "repack", "-adf", "-q")
	return f
}

// Hostile builds a bare repo whose root tree has entries git normally refuses
// to check out ("..", ".git"), created with plumbing. Returns the repo and commit.
func Hostile(t testing.TB) (bare string, commit object.OID) {
	t.Helper()
	tmp := t.TempDir()
	bare = filepath.Join(tmp, "hostile.git")
	Git(t, tmp, nil, "init", "-q", "--bare", bare)
	Git(t, bare, nil, "config", "uploadpack.allowFilter", "true")
	Git(t, bare, nil, "config", "uploadpack.allowAnySHA1InWant", "true")
	blob := Git(t, bare, []byte("evil\n"), "hash-object", "-w", "--stdin")
	good := Git(t, bare, []byte("good\n"), "hash-object", "-w", "--stdin")
	var tree bytes.Buffer
	for _, e := range []struct{ mode, name, oid string }{
		{"100644", ".git", blob}, {"100644", "..", blob}, {"100644", "good.txt", good},
	} {
		fmt.Fprintf(&tree, "%s %s\x00", e.mode, e.name)
		raw := make([]byte, 20)
		fmt.Sscanf(e.oid, "%x", &raw)
		tree.Write(raw)
	}
	tid := Git(t, bare, tree.Bytes(), "hash-object", "-w", "-t", "tree", "--literally", "--stdin")
	c := Git(t, bare, nil, "commit-tree", "-m", "hostile", tid)
	Git(t, bare, nil, "update-ref", "refs/heads/main", c)
	Git(t, bare, nil, "symbolic-ref", "HEAD", "refs/heads/main")
	commit, _ = object.ParseOID(c)
	return bare, commit
}

// NewWide builds a bare repo with n small files under w/ (each containing "needle")
// and a few outside, for testing batching independent of file count.
func NewWide(t testing.TB, n int) string {
	t.Helper()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	os.MkdirAll(filepath.Join(work, "w"), 0o755)
	Git(t, work, nil, "init", "-q", "-b", "main")
	for i := 0; i < n; i++ {
		write(t, work, fmt.Sprintf("w/f%05d.txt", i), []byte(fmt.Sprintf("line needle %d\n", i)), 0o644)
	}
	write(t, work, "other/o.txt", []byte("other\n"), 0o644)
	Git(t, work, nil, "add", "-A")
	Git(t, work, nil, "commit", "-q", "-m", "wide")
	bare := filepath.Join(tmp, "wide.git")
	Git(t, tmp, nil, "clone", "-q", "--bare", work, bare)
	Git(t, bare, nil, "config", "uploadpack.allowFilter", "true")
	Git(t, bare, nil, "config", "uploadpack.allowAnySHA1InWant", "true")
	Git(t, bare, nil, "repack", "-adf", "-q")
	return bare
}
