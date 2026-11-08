package shell_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/autowert66/explorerepo/internal/testutil"
)

func TestPullMatchesGitArchive(t *testing.T) {
	f := testutil.NewFixture(t)
	dest := filepath.ToSlash(filepath.Join(t.TempDir(), "all")) // a bare \ is a shell escape
	r := runAt(t, f.Bare, "", "HEAD", "pull / "+dest)
	if r.status != 0 {
		t.Fatalf("status %d stderr %s", r.status, r.err)
	}
	if !strings.HasPrefix(r.out, "pulled 19 files (") || !strings.HasSuffix(r.out, "to "+dest+"\n") {
		t.Errorf("out %q", r.out)
	}
	// regular files identical to the tree at HEAD
	ls := testutil.Git(t, f.Bare, nil, "ls-tree", "-r", "-z", "HEAD")
	n := 0
	for _, ent := range strings.Split(ls, "\x00") {
		if ent == "" {
			continue
		}
		meta, name, _ := strings.Cut(ent, "\t")
		fields := strings.Fields(meta)
		mode := fields[0]
		if mode == "120000" || mode == "160000" {
			continue
		}
		want := testutil.Git(t, f.Bare, nil, "cat-file", "blob", fields[2])
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(got)) != strings.TrimSpace(want) && !bytes.Equal(got, []byte(want)) {
			t.Errorf("%s differs", name)
		}
		st, _ := os.Stat(filepath.Join(dest, name))
		if (mode == "100755") != (st.Mode()&0o111 != 0) {
			t.Errorf("%s: exec bit wrong (mode %s, got %v)", name, mode, st.Mode())
		}
		n++
	}
	if n != 12 {
		t.Errorf("compared %d files", n)
	}
	// inside symlink preserved; escaping ones become regular files; submodule is an empty dir
	if tgt, err := os.Readlink(filepath.Join(dest, "link-to-readme")); err != nil || tgt != "README.md" {
		t.Errorf("link-to-readme: %q %v", tgt, err)
	}
	for _, name := range []string{"escape", "abs"} {
		st, err := os.Lstat(filepath.Join(dest, name))
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s should be a regular file: %v %v", name, st, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "escape")); string(b) != "../../etc/passwd" {
		t.Errorf("escape content %q", b)
	}
	if st, err := os.Stat(filepath.Join(dest, "sub")); err != nil || !st.IsDir() {
		t.Errorf("sub: %v %v", st, err)
	}
	if !strings.Contains(r.err, "points outside the destination") || !strings.Contains(r.err, "skipping submodule sub") {
		t.Errorf("warnings missing: %q", r.err)
	}
	if _, err := os.Lstat(filepath.Join(dest, ".git")); err == nil {
		t.Error("unexpected .git")
	}
}

func TestPullSubtreeAndFile(t *testing.T) {
	f := testutil.NewFixture(t)
	tmp := filepath.ToSlash(t.TempDir())
	r := runAt(t, f.Bare, "", "HEAD", "pull a/b "+tmp+"/b; pull a/a.txt "+tmp+"/one.txt")
	if r.status != 0 {
		t.Fatalf("status %d: %s", r.status, r.err)
	}
	b, err := os.ReadFile(filepath.Join(tmp, "b", "c", "d", "needle.txt"))
	if err != nil || !strings.Contains(string(b), "needle") {
		t.Errorf("needle: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(tmp, "one.txt")); string(b) != "alpha v2\n" {
		t.Errorf("one.txt = %q", b)
	}
}

func TestPullDefaultDestAndOverwrite(t *testing.T) {
	f := testutil.NewFixture(t)
	tmp := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(old)
	r := runAt(t, f.Bare, "", "HEAD", "cd a/b && pull c")
	if r.status != 0 || !strings.Contains(r.out, "to ./c") {
		t.Fatalf("status %d out %q err %q", r.status, r.out, r.err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "c", "c.txt")); err != nil {
		t.Error(err)
	}
	// existing destination is refused without -f, accepted with it
	r = runAt(t, f.Bare, "", "HEAD", "pull a/b/c")
	if r.status == 0 || !strings.Contains(r.err, "already exists (use -f to overwrite)") {
		t.Errorf("expected refusal, status %d err %q", r.status, r.err)
	}
	os.WriteFile(filepath.Join(tmp, "c", "c.txt"), []byte("local edit"), 0o644)
	r = runAt(t, f.Bare, "", "HEAD", "pull -f a/b/c")
	if r.status != 0 {
		t.Fatalf("status %d err %q", r.status, r.err)
	}
	if b, _ := os.ReadFile(filepath.Join(tmp, "c", "c.txt")); string(b) != "charlie\n" {
		t.Errorf("not overwritten: %q", b)
	}
}

func TestPullDestinationSymlink(t *testing.T) {
	f := testutil.NewFixture(t)
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, "dest")
	if err := os.Symlink(outside, dest); err != nil {
		t.Skip(err)
	}
	r := runAt(t, f.Bare, "", "HEAD", "pull -f README.md "+filepath.ToSlash(dest))
	if r.status == 0 {
		t.Fatalf("pull wrote through a destination symlink: %q", r.out)
	}
	if b, _ := os.ReadFile(outside); string(b) != "ORIGINAL\n" {
		t.Fatalf("symlink target overwritten: %q", b)
	}
}

func TestPullRefusesSymlinkedParent(t *testing.T) {
	f := testutil.NewFixture(t)
	tmp := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	dest := filepath.Join(link, "new")
	r := runAt(t, f.Bare, "", "HEAD", "pull a/b "+filepath.ToSlash(dest))
	if r.status == 0 {
		t.Fatalf("pull wrote through a symlinked parent: %q", r.out)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("wrote through symlinked parent: %v", ents)
	}
}

func TestPullUnicodeNormalizationCollision(t *testing.T) {
	bare := testutil.NewUnicodeCollision(t)
	probe := t.TempDir()
	if err := os.WriteFile(filepath.Join(probe, "é"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(probe, "e\u0301")); err != nil {
		t.Skip("filesystem distinguishes NFC and NFD names")
	}
	dest := filepath.Join(t.TempDir(), "u")
	r := runAt(t, bare, "", "HEAD", "pull / "+filepath.ToSlash(dest))
	if r.status == 0 {
		t.Fatalf("pull silently collapsed normalization-equivalent names: %q", r.out)
	}
}

func TestPullBackslashName(t *testing.T) {
	bare := testutil.NewBackslash(t)
	dest := filepath.Join(t.TempDir(), "b")
	r := runAt(t, bare, "", "HEAD", "pull / "+filepath.ToSlash(dest))
	if runtime.GOOS == "windows" {
		if r.status == 0 {
			t.Fatalf("pull created a backslash name on Windows: %q", r.out)
		}
		return
	}
	if r.status != 0 {
		t.Fatalf("status %d err %q", r.status, r.err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, `back\slash.txt`)); err != nil || string(b) != "backslash\n" {
		t.Fatalf("backslash file: %q %v", b, err)
	}
}

func TestPullDirectSubmodule(t *testing.T) {
	f := testutil.NewFixture(t)
	dest := filepath.Join(t.TempDir(), "sub")
	r := runAt(t, f.Bare, "", "HEAD", "pull sub "+filepath.ToSlash(dest))
	if r.status != 0 {
		t.Fatalf("status %d err %q", r.status, r.err)
	}
	st, err := os.Stat(dest)
	if err != nil || !st.IsDir() {
		t.Fatalf("submodule destination: %v %v", st, err)
	}
	if ents, _ := os.ReadDir(dest); len(ents) != 0 {
		t.Fatalf("submodule destination is not empty: %v", ents)
	}
	if !strings.Contains(r.err, "skipping submodule") {
		t.Errorf("missing submodule warning: %q", r.err)
	}
}

func TestPullRefusesThroughSymlink(t *testing.T) {
	f := testutil.NewFixture(t)
	tmp := t.TempDir()
	outside := t.TempDir()
	dest := filepath.ToSlash(filepath.Join(tmp, "dest"))
	os.MkdirAll(dest, 0o755)
	if err := os.Symlink(outside, filepath.Join(dest, "c")); err != nil { // dest/c -> elsewhere
		t.Skip(err)
	}
	r := runAt(t, f.Bare, "", "HEAD", "pull -f a/b "+dest)
	if r.status == 0 || !strings.Contains(r.err, "refusing to write through") {
		t.Errorf("status %d err %q", r.status, r.err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrote through symlink: %v", entries)
	}
}
