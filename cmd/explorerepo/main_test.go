package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/autowert66/explorerepo/internal/testutil"
)

func exec(t *testing.T, stdin string, args ...string) (out, errOut string, code int) {
	t.Helper()
	t.Setenv("EXPLOREREPO_CACHE", t.TempDir())
	var o, e bytes.Buffer
	code = run(args, strings.NewReader(stdin), &o, &e)
	return o.String(), e.String(), code
}

func TestModes(t *testing.T) {
	f := testutil.NewFixture(t)
	base := []string{"--repo", f.Bare}
	with := func(rest ...string) []string { return append(append([]string{}, base...), rest...) }

	out, _, code := exec(t, "", with("-c", "cat a/a.txt")...)
	if out != "alpha v2\n" || code != 0 {
		t.Errorf("-c: %q %d", out, code)
	}
	out, _, code = exec(t, "", with("cat", "a/a.txt")...)
	if out != "alpha v2\n" || code != 0 {
		t.Errorf("argv: %q %d", out, code)
	}
	// argv form needs no quoting: the pattern reaches grep untouched
	out, _, code = exec(t, "", with("grep", "-rn", "needle in", "a")...)
	if out != "a/b/c/d/needle.txt:2:needle in a haystack\n" || code != 0 {
		t.Errorf("argv grep: %q %d", out, code)
	}
	out, _, code = exec(t, "cd a\nls | wc -l\nexit 3\n", base...)
	if out != "4\n" || code != 3 {
		t.Errorf("stdin script: %q %d", out, code)
	}
	out, _, code = exec(t, "", with("--rev", "v1", "-c", "cat a/a.txt")...)
	if out != "alpha\n" || code != 0 {
		t.Errorf("--rev: %q %d", out, code)
	}
	out, _, code = exec(t, "", with("--hash", f.Commit1.String(), "cat", "a/a.txt")...)
	if out != "alpha\n" || code != 0 {
		t.Errorf("--hash: %q %d", out, code)
	}
	t.Setenv("EXPLOREREPO_REPO", f.Bare)
	out, _, code = exec(t, "", "cat", "a/a.txt")
	if out != "alpha v2\n" || code != 0 {
		t.Errorf("EXPLOREREPO_REPO: %q %d", out, code)
	}
}

func TestExitCodes(t *testing.T) {
	f := testutil.NewFixture(t)
	cases := []struct {
		args []string
		code int
		err  string
	}{
		{[]string{}, 128, "--repo is required"},
		{[]string{"--repo", f.Bare, "--rev", "nope", "ls"}, 128, `unknown revision "nope"`},
		{[]string{"--repo", f.Bare, "--rev", f.Commit1.String()[:10], "ls"}, 128, "use a full 40-character OID"},
		{[]string{"--repo", "/does/not/exist", "ls"}, 128, "does not appear to be a git repository"},
		{[]string{"--repo", f.Bare, "--max-output", "abc", "ls"}, 128, "invalid size"},
		{[]string{"--repo", f.Bare, "nosuchcmd"}, 127, "command not available"},
		{[]string{"--repo", f.Bare, "cat", "nope"}, 1, "No such file or directory"},
		{[]string{"--repo", f.Bare, "-c", "exit 7"}, 7, ""},
		{[]string{"--repo", f.Bare, "-c", "false"}, 1, ""},
		{[]string{"--repo", f.Bare, "-c", "if then"}, 2, "syntax error"},
		{[]string{"--repo", f.Bare, "grep", "needle", "a/b/c/d/d.txt"}, 1, ""},
	}
	for _, c := range cases {
		_, errOut, code := exec(t, "", c.args...)
		if code != c.code || !strings.Contains(errOut, c.err) {
			t.Errorf("%v: code %d (want %d) stderr %q", c.args, code, c.code, errOut)
		}
	}
}

func TestHelpNeedsNoRepo(t *testing.T) {
	out, _, code := exec(t, "", "help")
	if code != 0 || !strings.Contains(out, "grep") || !strings.Contains(out, "pull") {
		t.Errorf("help: %d %q", code, out[:min(80, len(out))])
	}
	if n := strings.Count(out, "\n"); n > 60 {
		t.Errorf("help is %d lines; keep it under ~60", n)
	}
	out, _, _ = exec(t, "", "help", "pull")
	if !strings.Contains(out, "REAL disk") {
		t.Errorf("help pull: %q", out)
	}
}

func TestHelpListsOnlyWorkingCommands(t *testing.T) {
	f := testutil.NewFixture(t)
	help, _, _ := exec(t, "", "help")
	for _, name := range []string{"cat", "head", "tail", "sed", "wc", "nl", "stat", "ls", "tree", "find", "grep", "rg",
		"sort", "uniq", "cut", "tr", "xargs", "basename", "dirname", "tee", "refs", "log", "git", "pull"} {
		if !strings.Contains(help, name) {
			t.Errorf("help does not mention %s", name)
		}
		_, errOut, code := exec(t, "", "--repo", f.Bare, "-c", name+" --help-flag-that-does-not-exist </dev/null >/dev/null")
		if code == 127 || strings.Contains(errOut, "command not available") {
			t.Errorf("%s listed in help but not available", name)
		}
	}
}

func TestByteSizeFlag(t *testing.T) {
	cases := map[string]int64{"100": 100, "1K": 1024, "2k": 2048, "3M": 3 << 20, "1G": 1 << 30, "5MB": 5 << 20}
	for in, want := range cases {
		var b byteSize
		if err := b.Set(in); err != nil || int64(b) != want {
			t.Errorf("%s: %d %v", in, b, err)
		}
	}
	for _, bad := range []string{"", "abc", "-1", "1X"} {
		var b byteSize
		if err := b.Set(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestMaxOutputFlag(t *testing.T) {
	f := testutil.NewFixture(t)
	out, errOut, code := exec(t, "", "--repo", f.Bare, "--max-output", "2K", "cat", "data/big.txt")
	if len(out) != 2048 || code != 0 || !strings.Contains(errOut, "output truncated at 2 KiB") {
		t.Errorf("len %d code %d stderr %q", len(out), code, errOut)
	}
}

func TestDebugCommands(t *testing.T) {
	f := testutil.NewFixture(t)
	out, _, code := exec(t, "", "--repo", f.Bare, "debug", "lsrefs", "HEAD", "refs/tags/")
	if code != 0 || !strings.Contains(out, f.Commit2.String()+" HEAD symref-target:refs/heads/main") || !strings.Contains(out, "peeled:"+f.Commit1.String()) {
		t.Errorf("lsrefs: %d\n%s", code, out)
	}
	out, _, code = exec(t, "", "--repo", f.Bare, "debug", "fetch", f.Commit2.String(), "--filter", "tree:0", "--deepen", "1")
	if code != 0 || !strings.Contains(out, f.Commit2.String()+" commit") || !strings.Contains(out, "# 1 objects") {
		t.Errorf("fetch: %d\n%s", code, out)
	}
}
