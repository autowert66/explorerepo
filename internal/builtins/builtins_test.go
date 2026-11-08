package builtins

import (
	"bytes"
	"context"
	"github.com/autowert66/explorerepo/internal/color"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestParseOpts(t *testing.T) {
	opts := []Opt{{Short: 'r'}, {Short: 'n'}, {Short: 'i'}, {Short: 'e', Arg: true}, {Short: 'A', Arg: true}, {Long: "include", Arg: true}, {Long: "color"}}
	cases := []struct {
		in   []string
		has  []string
		vals map[string]string
		args []string
	}{
		{[]string{"-rni", "pat", "dir"}, []string{"r", "n", "i"}, nil, []string{"pat", "dir"}},
		{[]string{"-A", "3", "pat"}, nil, map[string]string{"A": "3"}, []string{"pat"}},
		{[]string{"-A3", "pat"}, nil, map[string]string{"A": "3"}, []string{"pat"}},
		{[]string{"-rnA2", "pat"}, []string{"r", "n"}, map[string]string{"A": "2"}, []string{"pat"}},
		{[]string{"--include=*.go", "pat"}, nil, map[string]string{"include": "*.go"}, []string{"pat"}},
		{[]string{"--include", "*.go", "pat"}, nil, map[string]string{"include": "*.go"}, []string{"pat"}},
		{[]string{"pat", "-n", "file"}, []string{"n"}, nil, []string{"pat", "file"}}, // options after operands
		{[]string{"-e", "a", "-e", "b", "x"}, nil, map[string]string{"e": "b"}, []string{"x"}},
		{[]string{"--", "-n", "x"}, nil, nil, []string{"-n", "x"}},
		{[]string{"-", "x"}, nil, nil, []string{"-", "x"}}, // lone dash is an operand
	}
	for _, c := range cases {
		p, err := ParseOpts(c.in, opts, "")
		if err != nil {
			t.Errorf("%v: %v", c.in, err)
			continue
		}
		for _, k := range c.has {
			if !p.Has(k) {
				t.Errorf("%v: missing %s", c.in, k)
			}
		}
		for k, v := range c.vals {
			if p.Val(k) != v {
				t.Errorf("%v: %s = %q, want %q", c.in, k, p.Val(k), v)
			}
		}
		if !reflect.DeepEqual(append([]string(nil), p.Args...), append([]string(nil), c.args...)) {
			t.Errorf("%v: args %v, want %v", c.in, p.Args, c.args)
		}
	}
	if p, _ := ParseOpts([]string{"e", "-5"}, []Opt{{Short: 'n', Arg: true}}, "n"); p.Val("n") != "5" {
		t.Error("-NUM should map to -n NUM")
	}
}

func TestParseOptsErrors(t *testing.T) {
	opts := []Opt{{Short: 'a'}, {Short: 'b', Arg: true}, {Long: "long"}}
	for _, in := range [][]string{{"-z"}, {"--nope"}, {"-b"}, {"--long=1"}, {"-ab"}} {
		if _, err := ParseOpts(in, opts, ""); err == nil {
			t.Errorf("%v: expected error", in)
		}
	}
	_, err := ParseOpts([]string{"-z"}, opts, "")
	if err == nil || !strings.Contains(err.Error(), "unsupported option '-z' (supported: -a -b --long)") {
		t.Errorf("message: %v", err)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		fold   bool
		want   bool
	}{
		{"*.go", "main.go", false, true},
		{"*.go", "main.go.bak", false, false},
		{"*.go", ".hidden.go", false, true},
		{"a?c", "abc", false, true},
		{"a?c", "ac", false, false},
		{"[a-c]x", "bx", false, true},
		{"[!a-c]x", "bx", false, false},
		{"[!a-c]x", "dx", false, true},
		{"*", "a/b/c", false, true}, // * crosses '/'
		{"a/*/c", "a/b/b/c", false, true},
		{"README*", "readme.md", true, true},
		{"README*", "readme.md", false, false},
		{`a\*b`, "a*b", false, true},
		{`a\*b`, "axb", false, false},
		{"[", "[", false, true},
		{"", "", false, true},
		{"*a*b*", "xxaxxbxx", false, true},
	}
	for _, c := range cases {
		if got := GlobMatch(c.pat, c.s, c.fold); got != c.want {
			t.Errorf("GlobMatch(%q, %q, %v) = %v", c.pat, c.s, c.fold, got)
		}
	}
}

func TestBREtoERE(t *testing.T) {
	cases := map[string]string{
		`a\|b`:        `a|b`,
		`\(ab\)\+`:    `(ab)+`,
		`a+`:          `a\+`,
		`(x)`:         `\(x\)`,
		`a\{2,3\}`:    `a{2,3}`,
		`a{2}`:        `a\{2\}`,
		`\<word\>`:    `\bword\b`,
		`[a|b]`:       `[a|b]`,
		`[]a]+`:       `[]a]\+`,
		`[[:alpha:]]`: `[[:alpha:]]`,
		`a.*b`:        `a.*b`,
		`\.`:          `\.`,
	}
	for in, want := range cases {
		if got := breToERE(in); got != want {
			t.Errorf("breToERE(%q) = %q, want %q", in, got, want)
		}
		if _, err := regexp.Compile(breToERE(in)); err != nil {
			t.Errorf("breToERE(%q) does not compile: %v", in, err)
		}
	}
}

func TestExpandSet(t *testing.T) {
	got, err := expandSet(`a-cx\n`)
	if err != nil || string(got) != "abcx\n" {
		t.Errorf("%q %v", string(got), err)
	}
	if got, _ := expandSet("[:digit:]"); len(got) != 10 {
		t.Errorf("digits: %d", len(got))
	}
	if _, err := expandSet("z-a"); err == nil {
		t.Error("reverse range should fail")
	}
	if _, err := expandSet("[:nope:]"); err == nil {
		t.Error("unknown class should fail")
	}
}

// A set that starts with the class opener but closes immediately ("[:]") is
// malformed. It must be rejected or treated literally, never panic.
func TestExpandSetUnterminatedClass(t *testing.T) {
	for _, in := range []string{"[:]", "a[:]b", "[:]x"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("expandSet(%q) panicked: %v", in, r)
				}
			}()
			_, _ = expandSet(in)
		}()
	}
}

func TestParseListAndKey(t *testing.T) {
	rs, err := parseList("1,3-4,6-")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]bool{1: true, 2: false, 3: true, 4: true, 5: false, 6: true, 99: true} {
		if inList(rs, i) != want {
			t.Errorf("inList(%d) != %v", i, want)
		}
	}
	if _, err := parseList("0"); err == nil {
		t.Error("0 should be invalid")
	}
	k, err := parseKey("2,3nr", sortSpec{})
	if err != nil || k.start != 2 || k.end != 3 || !k.numeric || !k.reverse {
		t.Errorf("%+v %v", k, err)
	}
	if got := (sortSpec{start: 2, end: 3}).extract("a b c d", ""); got != "b c" {
		t.Errorf("extract = %q", got)
	}
	if got := (sortSpec{start: 2}).extract("a:b:c", ":"); got != "b:c" {
		t.Errorf("extract = %q", got)
	}
}

func TestSplitShellWords(t *testing.T) {
	got, err := splitShellWords(`a "b c" 'd e'  f`)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b c", "d e", "f"}) {
		t.Errorf("%q %v", got, err)
	}
	if _, err := splitShellWords(`a "b`); err == nil {
		t.Error("unmatched quote should fail")
	}
}

func TestIsBinary(t *testing.T) {
	if IsBinary([]byte("hello\n")) || !IsBinary([]byte("he\x00llo")) {
		t.Error("binary detection wrong")
	}
	big := make([]byte, 9000)
	for i := range big {
		big[i] = 'a'
	}
	big[8500] = 0
	if IsBinary(big) {
		t.Error("NUL after the first 8000 bytes should not count")
	}
}

func TestInsideDest(t *testing.T) {
	cases := []struct {
		rel, target string
		want        bool
	}{
		{"a/link", "b", true}, {"a/link", "../b", true}, {"a/link", "../../b", false},
		{"link", "../x", false}, {"link", "/etc/passwd", false}, {"a/b/link", "../../c", true},
	}
	for _, c := range cases {
		if got := insideDest(c.rel, c.target); got != c.want {
			t.Errorf("insideDest(%q,%q) = %v", c.rel, c.target, got)
		}
	}
}

func TestPrintf(t *testing.T) {
	cases := []struct {
		args []string
		out  string
		err  string
		code int
	}{
		{[]string{"%s\n", "a"}, "a\n", "", 0},
		{[]string{"%s-%s\n", "a", "b", "c", "d"}, "a-b\nc-d\n", "", 0},
		{[]string{"%d/%s\n", "3", "x"}, "3/x\n", "", 0},
		{[]string{"%05d|\n", "42"}, "00042|\n", "", 0},
		{[]string{"%-4d|\n", "7"}, "7   |\n", "", 0},
		{[]string{"%x %X %o %u\n", "255", "255", "8", "5"}, "ff FF 10 5\n", "", 0},
		{[]string{"%#x\n", "255"}, "0xff\n", "", 0},
		{[]string{"%.3s\n", "abcdef"}, "abc\n", "", 0},
		{[]string{"%8.3f\n", "3.14159"}, "   3.142\n", "", 0},
		{[]string{"%e\n", "1234.5"}, "1.234500e+03\n", "", 0},
		{[]string{"%b!\n", `a\tb`}, "a\tb!\n", "", 0},
		{[]string{"%b", `x\cy`}, "x", "", 0},
		{[]string{`a\tb\n`}, "a\tb\n", "", 0},
		{[]string{`\101\x42\u00e9\n`}, "ABé\n", "", 0},
		{[]string{"%%\n"}, "%\n", "", 0},
		{[]string{"%q\n", "a b"}, "'a b'\n", "", 0},
		{[]string{"%c\n", "hello"}, "h\n", "", 0},
		{[]string{"%5s|\n", "ab"}, "   ab|\n", "", 0},
		{[]string{"%*d\n", "4", "5"}, "   5\n", "", 0},
		{[]string{"%d\n", "'A"}, "65\n", "", 0},
		{[]string{"%d\n", "abc"}, "0\n", "printf: abc: expected a numeric value\n", 1},
		{[]string{"%d\n"}, "0\n", "", 0},
		{[]string{"x\\cy"}, "x", "", 0},
		{[]string{}, "", "printf: usage: printf FORMAT [ARGUMENTS...]\n", 2},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		env := &Env{Stdin: strings.NewReader(""), Stdout: NewOut(&out), Stderr: &errb, Cfg: &Config{}}
		code := printfCmd(context.Background(), env, c.args)
		env.Stdout.Flush()
		if out.String() != c.out || errb.String() != c.err || code != c.code {
			t.Errorf("printf %v = (%q, %q, %d), want (%q, %q, %d)",
				c.args, out.String(), errb.String(), code, c.out, c.err, c.code)
		}
	}
}

func TestBadComponent(t *testing.T) {
	for _, c := range []string{"", ".", "..", ".git", ".GIT", ".Git", "a/b", "a\x00b"} {
		if badComponent(c) == nil {
			t.Errorf("%q should be refused", c)
		}
	}
	if runtime.GOOS == "windows" {
		if badComponent(`a\b`) == nil {
			t.Errorf("a\\b should be refused on Windows")
		}
	} else if err := badComponent(`a\b`); err != nil {
		t.Errorf("a\\b is a legal POSIX name: %v", err)
	}
	for _, c := range []string{"a", ".github", ".gitignore", "git"} {
		if err := badComponent(c); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
}

// TestHelpStyledRoundTrip: styling only adds escapes, for every page.
func TestHelpStyledRoundTrip(t *testing.T) {
	on := Styler{On: true}
	for _, args := range [][]string{nil, {"repos"}, {"flags"}, {"about"}, {"pull"}, {"which"}, {"log"}, {"grep"}} {
		text, ok := Help(args)
		if got := HelpStyled(on, args, text, ok); color.Strip(got) != text || got == text {
			t.Errorf("help %v: styled text does not strip back to the original", args)
		}
	}
}
