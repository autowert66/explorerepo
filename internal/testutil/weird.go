package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/autowert66/explorerepo/internal/object"
)

// RawEntry is one entry of a tree built with plumbing. Names are strings, so
// invalid UTF-8 and names with backslashes are representable.
type RawEntry struct {
	Mode string
	Name string
	OID  object.OID
}

func newRawRepo(t testing.TB, name string) string {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, name+".git")
	Git(t, tmp, nil, "init", "-q", "--bare", bare)
	Git(t, bare, nil, "config", "uploadpack.allowFilter", "true")
	Git(t, bare, nil, "config", "uploadpack.allowAnySHA1InWant", "true")
	return bare
}

func parseOID(t testing.TB, s string) object.OID {
	t.Helper()
	o, err := object.ParseOID(s)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// RawTree writes a tree object. Entries are sorted by git's tree key (a
// directory sorts as name+"/"); equal names keep their input order.
func RawTree(t testing.TB, repo string, entries []RawEntry) object.OID {
	t.Helper()
	es := append([]RawEntry(nil), entries...)
	sort.SliceStable(es, func(i, j int) bool {
		return rawSortKey(es[i]) < rawSortKey(es[j])
	})
	var body []byte
	for _, e := range es {
		body = append(body, e.Mode...)
		body = append(body, ' ')
		body = append(body, e.Name...)
		body = append(body, 0)
		body = append(body, e.OID[:]...)
	}
	s := Git(t, repo, body, "hash-object", "-w", "-t", "tree", "--literally", "--stdin")
	return parseOID(t, s)
}

func rawSortKey(e RawEntry) string {
	if e.Mode == "40000" {
		return e.Name + "/"
	}
	return e.Name
}

// RawBlob writes a blob and returns its id.
func RawBlob(t testing.TB, repo string, data []byte) object.OID {
	t.Helper()
	return parseOID(t, Git(t, repo, data, "hash-object", "-w", "--stdin"))
}

// RawCommit creates a commit with a fixed author/committer date.
func RawCommit(t testing.TB, repo string, tree object.OID, parents []object.OID, msg, date string) object.OID {
	t.Helper()
	args := []string{"commit-tree", tree.String(), "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", p.String())
	}
	env := []string{
		"GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_DATE=" + date,
	}
	return parseOID(t, GitEnv(t, repo, nil, env, args...))
}

func setHeadMain(t testing.TB, repo string, commit object.OID) {
	t.Helper()
	Git(t, repo, nil, "update-ref", "refs/heads/main", commit.String())
	Git(t, repo, nil, "symbolic-ref", "HEAD", "refs/heads/main")
}

// NewOrdering builds a repository whose tree contains directory/file prefix
// pairs: a/ and a.txt, foo/ and foo.txt. Git's tree order differs from a raw
// name sort for these.
func NewOrdering(t testing.TB) string {
	t.Helper()
	repo := newRawRepo(t, "ordering")
	file := RawBlob(t, repo, []byte("file\n"))
	inside := RawBlob(t, repo, []byte("inside\n"))
	subA := RawTree(t, repo, []RawEntry{{Mode: "100644", Name: "inside.txt", OID: inside}})
	subFoo := RawTree(t, repo, []RawEntry{{Mode: "100644", Name: "inside.txt", OID: inside}})
	subBar := RawTree(t, repo, []RawEntry{{Mode: "100644", Name: "inside.txt", OID: inside}})
	tree := RawTree(t, repo, []RawEntry{
		{Mode: "40000", Name: "a", OID: subA},
		{Mode: "100644", Name: "a.txt", OID: file},
		{Mode: "40000", Name: "foo", OID: subFoo},
		{Mode: "40000", Name: "foo.bar", OID: subBar},
		{Mode: "100644", Name: "foo-bar.txt", OID: file},
		{Mode: "100644", Name: "foo.txt", OID: file},
	})
	setHeadMain(t, repo, RawCommit(t, repo, tree, nil, "ordering", "1700000000 +0000"))
	Git(t, repo, nil, "repack", "-adf", "-q")
	return repo
}

// NewDuplicate builds a tree with two entries both named "dup"; git reads the
// first one.
func NewDuplicate(t testing.TB) string {
	t.Helper()
	repo := newRawRepo(t, "duplicate")
	good := RawBlob(t, repo, []byte("good\n"))
	evil := RawBlob(t, repo, []byte("evil\n"))
	entries := []RawEntry{
		{Mode: "100644", Name: "dup", OID: good},
		{Mode: "100644", Name: "dup", OID: evil},
		{Mode: "100644", Name: "good.txt", OID: good},
	}
	// Enough siblings to make the unstable sort reorder equal names.
	for i := 0; i < 24; i++ {
		entries = append(entries, RawEntry{Mode: "100644", Name: fmt.Sprintf("f%02d", i), OID: good})
	}
	tree := RawTree(t, repo, entries)
	setHeadMain(t, repo, RawCommit(t, repo, tree, nil, "duplicate", "1700000000 +0000"))
	Git(t, repo, nil, "repack", "-adf", "-q")
	return repo
}

// NewUnicodeCollision builds a tree with the NFC and NFD spellings of the same
// filename. Case-sensitive filesystems can materialize both; APFS/HFS+ cannot.
func NewUnicodeCollision(t testing.TB) string {
	t.Helper()
	repo := newRawRepo(t, "unicode")
	nfc := RawBlob(t, repo, []byte("NFC\n"))
	nfd := RawBlob(t, repo, []byte("NFD\n"))
	tree := RawTree(t, repo, []RawEntry{
		{Mode: "100644", Name: "é.txt", OID: nfc},
		{Mode: "100644", Name: "e\u0301.txt", OID: nfd},
	})
	setHeadMain(t, repo, RawCommit(t, repo, tree, nil, "unicode", "1700000000 +0000"))
	Git(t, repo, nil, "repack", "-adf", "-q")
	return repo
}

// NewBackslash builds a tree with a POSIX-legal backslash filename.
func NewBackslash(t testing.TB) string {
	t.Helper()
	repo := newRawRepo(t, "backslash")
	ok := RawBlob(t, repo, []byte("ok\n"))
	bs := RawBlob(t, repo, []byte("backslash\n"))
	tree := RawTree(t, repo, []RawEntry{
		{Mode: "100644", Name: `back\slash.txt`, OID: bs},
		{Mode: "100644", Name: "ok.txt", OID: ok},
	})
	setHeadMain(t, repo, RawCommit(t, repo, tree, nil, "backslash", "1700000000 +0000"))
	Git(t, repo, nil, "repack", "-adf", "-q")
	return repo
}

// NewClockSkew returns a repo with two linear commits whose child is dated
// before its parent. Git still prints the child first.
func NewClockSkew(t testing.TB) (repo string, child, parent object.OID) {
	t.Helper()
	repo = newRawRepo(t, "skew")
	blob := RawBlob(t, repo, []byte("skew\n"))
	tree := RawTree(t, repo, []RawEntry{{Mode: "100644", Name: "f.txt", OID: blob}})
	parent = RawCommit(t, repo, tree, nil, "parent", "1700000000 +0000")
	child = RawCommit(t, repo, tree, []object.OID{parent}, "child", "100000000 +0000")
	setHeadMain(t, repo, child)
	Git(t, repo, nil, "repack", "-adf", "-q")
	return repo, child, parent
}

// NewTagChain builds n annotated tags, each pointing at the previous one, and
// returns the deepest tag object's id.
func NewTagChain(t testing.TB, n int) (repo string, commit, deepest object.OID) {
	t.Helper()
	repo = newRawRepo(t, "tagchain")
	blob := RawBlob(t, repo, []byte("tag chain\n"))
	tree := RawTree(t, repo, []RawEntry{{Mode: "100644", Name: "f.txt", OID: blob}})
	commit = RawCommit(t, repo, tree, nil, "tag chain", "1700000000 +0000")
	prev := commit.String()
	deepest = commit
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("chain%d", i+1)
		Git(t, repo, nil, "tag", "-a", "-m", name, name, prev)
		deepest = parseOID(t, Git(t, repo, nil, "rev-parse", name))
		prev = deepest.String()
	}
	setHeadMain(t, repo, commit)
	Git(t, repo, nil, "repack", "-adf", "-q")
	return repo, commit, deepest
}

// NewMergeOrder builds an octopus whose parents have identical dates. Git's
// log shows the first parent before the later parents even though those are
// its children, because the frontier is ordered by date with insertion order
// breaking ties.
func NewMergeOrder(t testing.TB) (repo string, want []object.OID) {
	t.Helper()
	repo = newRawRepo(t, "mergeorder")
	blob := RawBlob(t, repo, []byte("merge\n"))
	tree := RawTree(t, repo, []RawEntry{{Mode: "100644", Name: "f.txt", OID: blob}})
	date := "1700000000 +0000"
	root := RawCommit(t, repo, tree, nil, "root", date)
	first := RawCommit(t, repo, tree, []object.OID{root}, "first", date)
	second := RawCommit(t, repo, tree, []object.OID{first}, "second", date)
	third := RawCommit(t, repo, tree, []object.OID{first}, "third", date)
	head := RawCommit(t, repo, tree, []object.OID{first, second, third}, "head", date)
	setHeadMain(t, repo, head)
	Git(t, repo, nil, "repack", "-adf", "-q")
	return repo, []object.OID{head, first, second, third, root}
}

// NewUnborn builds a bare repository with no commits.
func NewUnborn(t testing.TB) string {
	t.Helper()
	repo := newRawRepo(t, "unborn")
	Git(t, repo, nil, "symbolic-ref", "HEAD", "refs/heads/main")
	return repo
}

// WriteFile is a helper for tests that need a real host file.
func WriteFile(t testing.TB, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
}
