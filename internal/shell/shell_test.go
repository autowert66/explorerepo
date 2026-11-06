package shell_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/autowert66/explorerepo/internal/builtins"
	"github.com/autowert66/explorerepo/internal/repotest"
	"github.com/autowert66/explorerepo/internal/shell"
	"github.com/autowert66/explorerepo/internal/testutil"
	"github.com/autowert66/explorerepo/internal/transport"
	"github.com/autowert66/explorerepo/internal/vfs"
)

type result struct {
	out, err string
	status   int
	ct       *transport.Counting
}

func opts(t *testing.T, bare, cache, rev string) (shell.Options, *transport.Counting, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	r, ct := repotest.Open(t, bare, cache, rev)
	var out, errb bytes.Buffer
	fs := vfs.New(r)
	fs.Warn = func(m string) { errb.WriteString("explorerepo: warning: " + m + "\n") }
	return shell.Options{
		Repo: r, FS: fs, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		Cfg: &builtins.Config{MaxFiles: 20000, RepoName: "fixture"},
	}, ct, &out, &errb
}

func runAt(t *testing.T, bare, cache, rev, script string) result {
	t.Helper()
	o, ct, out, errb := opts(t, bare, cache, rev)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res := shell.Run(ctx, o, script)
	return result{out.String(), errb.String(), res.Status, ct}
}

func run(t *testing.T, f *testutil.Fixture, script string) result {
	t.Helper()
	return runAt(t, f.Bare, "", "HEAD", script)
}

func expect(t *testing.T, script string, r result, wantOut string, wantStatus int) {
	t.Helper()
	if r.out != wantOut || r.status != wantStatus {
		t.Errorf("script: %s\n status %d (want %d)\n stdout:\n%s\n want:\n%s\n stderr: %s", script, r.status, wantStatus, r.out, wantOut, r.err)
	}
}

func TestBasicScripts(t *testing.T) {
	f := testutil.NewFixture(t)
	cases := []struct {
		script string
		out    string
		status int
	}{
		{"cat a/a.txt", "alpha v2\n", 0},
		{"cat a/b/c/d/d.txt | wc -l", "1\n", 0},
		{"find . -name '*.txt' | sort", "./a/a.txt\n./a/b/b.txt\n./a/b/c/c.txt\n./a/b/c/d/d.txt\n./a/b/c/d/needle.txt\n./data/big.txt\n./dir with space/file with space.txt\n./empty.txt\n./ünï/çödé.txt\n", 0},
		{"grep -rn needle a", "a/b/c/d/needle.txt:2:needle in a haystack\n", 0},
		{"grep -rn needle a | head -1", "a/b/c/d/needle.txt:2:needle in a haystack\n", 0},
		{"cd a/b && head -3 c/c.txt", "charlie\n", 0},
		{"cd a/b/c; pwd; cd ..; pwd", "/a/b/c\n/a/b\n", 0},
		{"find a -name '*.txt' | xargs cat", "alpha v2\nbravo\ncharlie\ndelta\nhay\nneedle in a haystack\nhay\n", 0},
		{"for f in a/*.txt; do echo $f; done", "a/a.txt\n", 0},
		{"[ -d a ] && echo dir; [ -f a/a.txt ] && echo file; [ -e nope ] || echo missing", "dir\nfile\nmissing\n", 0},
		{"ls a", "a.txt\nb\nlink-dir\nlink-up\n", 0},
		{"ls -F a", "a.txt\nb/\nlink-dir@\nlink-up@\n", 0},
		{"ls -d a/b", "a/b\n", 0},
		{"cat link-to-readme | head -1", "# Hello\n", 0},
		{"cat a/link-up | head -1", "# Hello\n", 0},
		{"cat a/link-dir/c.txt", "charlie\n", 0},
		{"head -n 1 README.md a/a.txt", "==> README.md <==\n# Hello\n\n==> a/a.txt <==\nalpha v2\n", 0},
		{"tail -n 1 README.md", "third line\n", 0},
		{"tail -n +2 README.md", "second line\nthird line\n", 0},
		{"sed -n '2,3p' README.md", "second line\nthird line\n", 0},
		{"sed -n '$p' README.md", "third line\n", 0},
		{"wc -l README.md", "3 README.md\n", 0},
		{"wc -l < README.md", "3\n", 0},
		{"wc -c README.md empty.txt", "31 README.md\n 0 empty.txt\n31 total\n", 0},
		{"cat empty.txt | wc -c", "0\n", 0},
		{"cat -n README.md", "     1\t# Hello\n     2\tsecond line\n     3\tthird line\n", 0},
		{"echo $REPO | grep -c fixture; echo done", "1\ndone\n", 0},
		{"grep -c needle data/bin.dat", "1\n", 0},
		{"grep needle data/bin.dat", "Binary file data/bin.dat matches\n", 0},
		{"grep -I needle data/bin.dat; echo $?", "1\n", 0},
		{"grep -rl needle | sort", "a/b/c/d/needle.txt\ndata/bin.dat\nünï/çödé.txt\n", 0},
		{"grep -rlI needle . | sort", "./a/b/c/d/needle.txt\n./ünï/çödé.txt\n", 0},
		{"grep -rn --include='*.txt' needle . | wc -l", "2\n", 0},
		{"grep -c . README.md", "3\n", 0},
		{"grep -n -A1 -B1 second README.md", "1-# Hello\n2:second line\n3-third line\n", 0},
		{"grep -ow 'l[a-z]*' README.md", "line\nline\n", 0},
		{"grep -E 'sec|thi' README.md | wc -l", "2\n", 0},
		{`grep 'sec\|thi' README.md | wc -l`, "2\n", 0},
		{"grep -F 'a.b' README.md; echo $?", "1\n", 0},
		{"grep -v line README.md", "# Hello\n", 0},
		{"rg -c needle a", "a/b/c/d/needle.txt:1\n", 0},
		{"rg -l needle", "a/b/c/d/needle.txt\nünï/çödé.txt\n", 0},
		{"rg -i HELLO README.md", "1:# Hello\n", 0},
		{"rg --files a | wc -l", "5\n", 0},
		{"find . -maxdepth 1 -type f | sort", "./README.md\n./empty.txt\n", 0},
		{"find . -maxdepth 1 -type l | sort", "./abs\n./escape\n./link-to-readme\n./loop1\n./loop2\n", 0},
		{"find a -type d", "a\na/b\na/b/c\na/b/c/d\n", 0},
		{"find a -path 'a/b/*' -name '*.txt' | sort", "a/b/b.txt\na/b/c/c.txt\na/b/c/d/d.txt\na/b/c/d/needle.txt\n", 0},
		{"find a ! -name '*.txt' -type f", "", 0},
		{"find a -name b -prune -o -type f -print", "a/a.txt\n", 0},
		{"find a -mindepth 2 -maxdepth 2", "a/b/b.txt\na/b/c\n", 0},
		{"find a \\( -name a.txt -o -name b.txt \\)", "a/a.txt\na/b/b.txt\n", 0},
		{"find a -name a.txt -exec cat {} \\;", "alpha v2\n", 0},
		{"find a -name '*.txt' -exec echo {} +", "a/a.txt a/b/b.txt a/b/c/c.txt a/b/c/d/d.txt a/b/c/d/needle.txt\n", 0},
		{"tree -L 1 a", "a\n├── a.txt\n├── b\n├── link-dir -> b/c\n└── link-up -> ../README.md\n\n1 directory, 3 files\n", 0},
		{"tree -d a", "a\n└── b\n    └── c\n        └── d\n\n3 directories\n", 0},
		{"basename a/b/c.txt .txt; dirname a/b/c.txt", "c\na/b\n", 0},
		{"printf 'b\\na\\nb\\n' | sort -u", "a\nb\n", 0},
		{"printf 'x 3\\ny 1\\nz 2\\n' | sort -k2 -n | cut -d' ' -f1", "y\nz\nx\n", 0},
		{"echo hello | tr 'a-y' 'b-z'", "ifmmp\n", 0},
		{"echo aabbcc | tr -s 'a-c'", "abc\n", 0},
		{"echo abc123 | tr -d '[:digit:]'", "abc\n", 0},
		{"printf 'a\\na\\nb\\n' | uniq -c", "      2 a\n      1 b\n", 0},
		{"printf 'a b\\n' | xargs -n1 echo", "a\nb\n", 0},
		{"printf 'a\\nb\\n' | xargs -I{} echo x{}y", "xay\nxby\n", 0},
		{"printf 'a b\\n' | xargs -n1 printf '[%s]\\n'", "[a]\n[b]\n", 0},
		{"printf 'a\\nb\\n' | xargs printf '%s|'", "a|b|", 0},
		{"echo hello | xargs printf '%s-%s\\n' x", "x-hello\n", 0},
		{"find a -name a.txt -exec printf '<%s>\\n' {} \\;", "<a/a.txt>\n", 0},
		{"echo a b c | tee /dev/null", "a b c\n", 0},
	}
	for _, c := range cases {
		r := run(t, f, c.script)
		expect(t, c.script, r, c.out, c.status)
	}
}

func TestErrorsAndLimits(t *testing.T) {
	f := testutil.NewFixture(t)
	cases := []struct {
		script   string
		stderr   string
		status   int
		emptyOut bool
	}{
		{"cat nope", "cat: nope: No such file or directory\n", 1, true},
		{"cat a", "cat: a: Is a directory\n", 1, true},
		{"ls nope", "ls: nope: No such file or directory\n", 1, true},
		{"cat escape", "cat: escape: symlink points outside repository\n", 1, true},
		{"cat abs", "cat: abs: symlink points outside repository\n", 1, true},
		{"cat loop1", "cat: loop1: Too many levels of symbolic links\n", 1, true},
		{"cat sub", "cat: sub: submodule (commit 0123456789abcdef0123456789abcdef01234567), not fetched\n", 1, true},
		{"cd sub", "cd: sub: submodule (commit 0123456789abcdef0123456789abcdef01234567), not fetched\n", 1, true},
		{"cd nope", "cd: nope: No such file or directory\n", 1, true},
		{"echo hi > out.txt", "sh: out.txt: read-only file system\n", 1, true},
		{"echo hi >> out.txt", "sh: out.txt: read-only file system\n", 1, true},
		{"rm -rf /", "explorerepo: rm: command not available (run 'help')\n", 127, true},
		{"/bin/ls", "explorerepo: /bin/ls: command not available (run 'help')\n", 127, true},
		{"curl http://example.com", "explorerepo: curl: command not available (run 'help')\n", 127, true},
		{"ls -z", "ls: unsupported option '-z' (supported: -a -l -1 -R -d -F -A)\n", 2, true},
		{"sed 's/a/b/' README.md", "sed: script \"s/a/b/\" is not supported (only sed -n 'A,Bp', 'Ap', 'A,$p' and 'Nq'; use head, tail or grep for the rest)\n", 2, true},
		{"find . -size +1k", "find: unsupported predicate '-size' (supported: -name -iname -path -ipath -regex -type f|d|l -maxdepth -mindepth -prune -print -print0 -exec ! -o -a ( ))\n", 2, true},
		{"git status", "git: only these git subcommands are available: log [-n N] [--oneline], show REV:PATH, ls-tree [-r] [-d] [--name-only] REV [PATH], rev-parse [REV]\n", 2, true},
		{"grep -r nothing-matches-this a", "", 1, true},
		{"cat < /etc/passwd", "sh: /etc/passwd: No such file or directory\n", 1, true},
	}
	for _, c := range cases {
		r := run(t, f, c.script)
		if r.status != c.status || r.err != c.stderr || (c.emptyOut && r.out != "") {
			t.Errorf("script %q:\n status %d (want %d)\n stderr %q\n want   %q\n stdout %q", c.script, r.status, c.status, r.err, c.stderr, r.out)
		}
	}
}

func TestSyntaxError(t *testing.T) {
	f := testutil.NewFixture(t)
	r := run(t, f, "if then")
	if r.status != 2 || !strings.Contains(r.err, "syntax error") {
		t.Errorf("status %d err %q", r.status, r.err)
	}
}

func TestListMultipleArgs(t *testing.T) {
	f := testutil.NewFixture(t)
	r := run(t, f, "ls bin a/b")
	expect(t, "ls bin a/b", r, "bin:\ntool.sh\n\na/b:\nb.txt\nc\n", 0)
	r = run(t, f, "ls bin/tool.sh a/b")
	expect(t, "ls bin/tool.sh a/b", r, "bin/tool.sh\n\na/b:\nb.txt\nc\n", 0)
}

func TestOutputTruncation(t *testing.T) {
	f := testutil.NewFixture(t)
	o, _, out, errb := opts(t, f.Bare, "", "HEAD")
	o.MaxOutput = 1024
	res := shell.Run(context.Background(), o, "cat data/big.txt; echo unreachable")
	if res.Status != 0 || !res.Truncated {
		t.Fatalf("status %d truncated %v", res.Status, res.Truncated)
	}
	if out.Len() != 1024 || strings.Contains(out.String(), "unreachable") {
		t.Errorf("stdout len %d", out.Len())
	}
	if !strings.Contains(errb.String(), "output truncated at 1 KiB") {
		t.Errorf("stderr %q", errb.String())
	}
}

func TestPipeClosureIsQuiet(t *testing.T) {
	f := testutil.NewFixture(t)
	r := run(t, f, "cat data/big.txt | head -2; find . | head -1")
	if r.err != "" || r.status != 0 {
		t.Errorf("status %d stderr %q", r.status, r.err)
	}
	if !strings.HasPrefix(r.out, "line 000000") || !strings.HasSuffix(r.out, "\n.\n") {
		t.Errorf("out %q", r.out)
	}
}

func TestMaxFiles(t *testing.T) {
	bare := testutil.NewWide(t, 50)
	o, ct, out, errb := opts(t, bare, "", "HEAD")
	o.Cfg.MaxFiles = 10
	before := ct.Requests("fetch")
	res := shell.Run(context.Background(), o, "grep -rn needle w")
	if res.Status != 2 || !strings.Contains(errb.String(), "too many files") || out.Len() != 0 {
		t.Fatalf("status %d stderr %q", res.Status, errb.String())
	}
	// no blob fetch happened: only trees (w) were needed
	for _, l := range ct.Log()[before:] {
		if strings.Contains(l, `filter=""`) {
			t.Errorf("blob fetch despite --max-files: %v", ct.Log())
		}
	}
}

func TestRevSelection(t *testing.T) {
	f := testutil.NewFixture(t)
	for rev, want := range map[string]string{
		"main":             "alpha v2\n",
		"feature":          "alpha\n",
		"v1":               "alpha\n", // annotated tag
		"v1-light":         "alpha\n",
		f.Commit1.String(): "alpha\n",
		f.TagObj.String():  "alpha\n",
		"refs/heads/main":  "alpha v2\n",
	} {
		r := runAt(t, f.Bare, "", rev, "cat a/a.txt")
		expect(t, "rev "+rev, r, want, 0)
	}
	r := runAt(t, f.Bare, "", "feature", "cat feature.txt")
	expect(t, "feature.txt", r, "feature branch\n", 0)
}

func TestEnvVars(t *testing.T) {
	f := testutil.NewFixture(t)
	r := run(t, f, `echo "$COMMIT"; echo "$PWD"; echo "[$PATH]"`)
	// The interpreter keeps $PWD in host form, so it carries the drive on
	// Windows ("C:\\"); pwd prints the virtual path either way.
	pwd := "/"
	if runtime.GOOS == "windows" {
		pwd, _ = filepath.Abs("/")
	}
	expect(t, "env", r, f.Commit2.String()+"\n"+pwd+"\n[]\n", 0)
}

func TestHostileTree(t *testing.T) {
	bare, _ := testutil.Hostile(t)
	r := runAt(t, bare, "", "HEAD", "ls -a; cat good.txt")
	if !strings.Contains(r.out, ".git") || strings.Contains(r.out, "..\n.git") && false {
		t.Errorf("out %q", r.out)
	}
	if !strings.Contains(r.err, `ignoring tree entry with invalid name ".."`) {
		t.Errorf("expected warning, stderr %q", r.err)
	}
	if !strings.Contains(r.out, "good\n") {
		t.Errorf("out %q", r.out)
	}
	// pull must refuse
	dest := filepath.Join(t.TempDir(), "out")
	r = runAt(t, bare, "", "HEAD", "pull / "+dest)
	if r.status == 0 || !strings.Contains(r.err, ".git") {
		t.Errorf("pull should refuse .git: status %d stderr %q", r.status, r.err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		t.Error(".git was written")
	}
}

// TestHelpIsOurs checks that "help" inside a script prints the explorerepo
// manual and topics, not the interpreter's own builtin list.
func TestHelpIsOurs(t *testing.T) {
	f := testutil.NewFixture(t)
	for script, want := range map[string]string{
		"help | head -1":       "explorerepo: a read-only shell",
		"help repos | head -1": "--repo SPEC forms",
		"help flags | head -1": "Options come before",
		"help pull | head -1":  "pull [-f] PATH [DEST]",
	} {
		r := run(t, f, script)
		if r.status != 0 || !strings.HasPrefix(r.out, want) {
			t.Errorf("%s: %d %q", script, r.status, r.out)
		}
	}
	if r := run(t, f, "help nonsense"); r.status == 0 {
		t.Errorf("help nonsense should fail: %d %q", r.status, r.out)
	}
}
