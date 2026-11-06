package vfs_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"explorerepo/internal/repotest"
	"explorerepo/internal/testutil"
	"explorerepo/internal/vfs"
)

var ctx = context.Background()

func newFS(t *testing.T) (*vfs.FS, *testutil.Fixture) {
	f := testutil.NewFixture(t)
	r, _ := repotest.Open(t, f.Bare, "", "HEAD")
	return vfs.New(r), f
}

func names(ns []*vfs.Node) string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return strings.Join(out, ",")
}

func TestReadDirRoot(t *testing.T) {
	v, _ := newFS(t)
	ns, err := v.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	got := names(ns)
	for _, want := range []string{"README.md", "a", "bin", "data", "sub", "link-to-readme", "ünï"} {
		if !strings.Contains(got, want) {
			t.Errorf("root missing %s: %s", want, got)
		}
	}
}

func TestReadFileAndPaths(t *testing.T) {
	v, _ := newFS(t)
	for _, p := range []string{"/a/b/c/d/d.txt", "a/b/c/d/d.txt", "/a/../a/b/c/d/d.txt", "./a//b/c/d/d.txt"} {
		b, err := v.ReadFile(ctx, p)
		if err != nil || string(b) != "delta\n" {
			t.Errorf("%s: %q %v", p, b, err)
		}
	}
	b, err := v.ReadFile(ctx, "/dir with space/file with space.txt")
	if err != nil || string(b) != "spaced\n" {
		t.Errorf("space: %q %v", b, err)
	}
	b, err = v.ReadFile(ctx, "/ünï/çödé.txt")
	if err != nil || string(b) != "unicode needle\n" {
		t.Errorf("unicode: %q %v", b, err)
	}
}

func TestErrors(t *testing.T) {
	v, f := newFS(t)
	if _, err := v.ReadFile(ctx, "/nope"); !errors.Is(err, fs.ErrNotExist) || err.Error() != "No such file or directory" {
		t.Errorf("missing: %v", err)
	}
	if _, err := v.ReadFile(ctx, "/a"); err != vfs.ErrIsDir {
		t.Errorf("dir read: %v", err)
	}
	if _, err := v.ReadFile(ctx, "/README.md/x"); err != vfs.ErrNotDir {
		t.Errorf("not a dir: %v", err)
	}
	var se *vfs.SubmoduleError
	if _, err := v.ReadDir(ctx, "/sub"); !errors.As(err, &se) {
		t.Errorf("gitlink readdir: %v", err)
	} else if !strings.Contains(err.Error(), "submodule (commit "+f.Gitlink.String()+"), not fetched") {
		t.Errorf("gitlink msg: %v", err)
	}
	n, err := v.Stat(ctx, "/sub")
	if err != nil || !n.IsGitlink() {
		t.Errorf("gitlink stat: %v %v", n, err)
	}
}

func TestSymlinks(t *testing.T) {
	v, _ := newFS(t)
	b, err := v.ReadFile(ctx, "/link-to-readme")
	if err != nil || !strings.HasPrefix(string(b), "# Hello") {
		t.Errorf("link-to-readme: %q %v", b, err)
	}
	b, err = v.ReadFile(ctx, "/a/link-up")
	if err != nil || !strings.HasPrefix(string(b), "# Hello") {
		t.Errorf("a/link-up: %q %v", b, err)
	}
	b, err = v.ReadFile(ctx, "/a/link-dir/d/d.txt") // symlink to b/c in the middle of a path
	if err != nil || string(b) != "delta\n" {
		t.Errorf("a/link-dir/d/d.txt: %q %v", b, err)
	}
	if _, err := v.ReadFile(ctx, "/escape"); err == nil || !strings.Contains(err.Error(), "outside repository") {
		t.Errorf("escape: %v", err)
	}
	if _, err := v.ReadFile(ctx, "/abs"); err == nil || !strings.Contains(err.Error(), "outside repository") {
		t.Errorf("abs: %v", err)
	}
	if _, err := v.ReadFile(ctx, "/loop1"); err != vfs.ErrLoop {
		t.Errorf("loop: %v", err)
	}
	n, err := v.Lstat(ctx, "/link-to-readme")
	if err != nil || !n.IsSymlink() {
		t.Errorf("lstat should not follow: %v %v", n, err)
	}
	if tgt, err := v.Readlink(ctx, "/link-to-readme"); err != nil || tgt != "README.md" {
		t.Errorf("readlink: %q %v", tgt, err)
	}
}

func TestHostileEntriesHidden(t *testing.T) {
	bare, _ := testutil.Hostile(t)
	r, _ := repotest.Open(t, bare, "", "HEAD")
	v := vfs.New(r)
	var warns []string
	v.Warn = func(m string) { warns = append(warns, m) }
	ns, err := v.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	// ".." is hidden; ".git" is a legal name for the vfs (pull refuses it)
	if got := names(ns); strings.Contains(got, "..,") || got != ".git,good.txt" {
		t.Errorf("entries = %s", got)
	}
	if len(warns) != 1 {
		t.Errorf("warnings = %v", warns)
	}
	v.ReadDir(ctx, "/")
	if len(warns) != 1 {
		t.Errorf("warning should be emitted once: %v", warns)
	}
}
