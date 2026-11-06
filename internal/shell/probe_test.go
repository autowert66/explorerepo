package shell_test

import (
	"testing"

	"github.com/autowert66/explorerepo/internal/testutil"
)

// TestProbes covers the availability probes agents use to decide whether a tool
// exists: type, command -v/-V, which, and the --version/--help stubs.
func TestProbes(t *testing.T) {
	f := testutil.NewFixture(t)
	needle := "a/b/c/d/needle.txt:2:needle in a haystack\n"
	cases := []struct {
		script string
		out    string
		status int
	}{
		{"type rg", "rg is a shell builtin\n", 0},
		{"type cd", "cd is a shell builtin\n", 0},
		{"type if", "if is a shell keyword\n", 0},
		{"type -t rg", "builtin\n", 0},
		{"type -t if", "keyword\n", 0},
		{"type -p rg", "rg\n", 0},
		{"type -P rg", "rg\n", 0},
		{"type -a rg", "rg is a shell builtin\n", 0},
		{"type rg cd if", "rg is a shell builtin\ncd is a shell builtin\nif is a shell keyword\n", 0},
		{"type -p nope", "", 1},
		{"type nope", "", 1},
		{"command -v rg", "rg\n", 0},
		{"command -v cd", "cd\n", 0},
		{"command -v if", "if\n", 0},
		{"command -v rg cd", "rg\ncd\n", 0},
		{"command -v nope", "", 1},
		{"command -V rg", "rg is a shell builtin\n", 0},
		{"command -V nope", "", 1},
		{"which rg", "rg\n", 0},
		{"which cd", "cd\n", 0},
		{"which -a rg", "rg\n", 0},
		{"which nope", "", 1},
		{"which rg nope", "rg\n", 1},
		{"which", "", 1},
		{"rg --version", "rg (explorerepo builtin)\n", 0},
		{"rg --help", "rg: ripgrep-style recursive search (-i -l -c -w -F -g --files)\nSee 'help' for the supported flags.\n", 0},
		{"command rg needle a", needle, 0},
		// The probe must not gate the tool off: this is the whole point.
		{"type -p rg >/dev/null && rg needle a", needle, 0},
		{"RG=$(command -v rg); $RG needle a", needle, 0},
		{"hash rg && echo yes", "yes\n", 0},
		// Unknown names still fall through to the interpreter (functions/aliases).
		{"foo() { echo hi; }; type foo", "foo is a function\n", 0},
	}
	for _, c := range cases {
		r := run(t, f, c.script)
		if r.out != c.out || r.status != c.status {
			t.Errorf("script %q:\n status %d (want %d)\n stdout %q\n want   %q\n stderr %q",
				c.script, r.status, c.status, r.out, c.out, r.err)
		}
	}
}

func TestProbeStderr(t *testing.T) {
	f := testutil.NewFixture(t)
	for _, c := range []struct {
		script string
		err    string
		status int
	}{
		{"type nope", "type: nope: not found\n", 1},
		{"command -V nope", "command: nope: not found\n", 1},
		{"which", "usage: which [-as] program ...\n", 1},
	} {
		r := run(t, f, c.script)
		if r.err != c.err || r.status != c.status || r.out != "" {
			t.Errorf("script %q:\n status %d (want %d)\n stderr %q\n want   %q\n stdout %q",
				c.script, r.status, c.status, r.err, c.err, r.out)
		}
	}
}
