// Package color decides whether output is for a human at a terminal and, if
// so, styles it with ANSI escapes. Agents and pipes always get plain text.
package color

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
)

// Modes accepted by --color.
const (
	Auto   = "auto"
	Always = "always"
	Never  = "never"
)

// ParseMode validates a --color value.
func ParseMode(s string) (string, error) {
	switch s {
	case Auto, Always, Never:
		return s, nil
	}
	return "", fmt.Errorf("--color must be auto, always or never, not %q", s)
}

// agentEnv are variables that agent harnesses set for the commands they run.
// Some of them run commands in a pseudo-terminal, which isatty cannot tell
// apart from a human's.
var agentEnv = []string{"CLAUDECODE", "CODEX_SANDBOX", "GEMINI_CLI", "CURSOR_AGENT"}

// Enabled reports whether output written to w should be colored. In auto
// mode, in order: NO_COLOR disables, CLICOLOR_FORCE or FORCE_COLOR enables,
// an agent harness or TERM=dumb disables, otherwise w must be a terminal.
func Enabled(w io.Writer, mode string) bool {
	switch mode {
	case Always:
		return true
	case Never:
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if set("CLICOLOR_FORCE") || set("FORCE_COLOR") {
		return true
	}
	for _, k := range agentEnv {
		if os.Getenv(k) != "" {
			return false
		}
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func set(k string) bool { v := os.Getenv(k); return v != "" && v != "0" && v != "false" }

// Styler applies styles when On and returns text unchanged otherwise, so
// callers style unconditionally.
type Styler struct{ On bool }

// Reset ends any style, for output cut off mid-escape.
const Reset = "\x1b[0m"

func (s Styler) wrap(code, t string) string {
	if !s.On || t == "" {
		return t
	}
	return "\x1b[" + code + "m" + t + Reset
}

func (s Styler) Bold(t string) string   { return s.wrap("1", t) }
func (s Styler) Dim(t string) string    { return s.wrap("2", t) }
func (s Styler) Red(t string) string    { return s.wrap("31", t) }
func (s Styler) Green(t string) string  { return s.wrap("32", t) }
func (s Styler) Yellow(t string) string { return s.wrap("33", t) }
func (s Styler) Cyan(t string) string   { return s.wrap("36", t) }
func (s Styler) Err(t string) string    { return s.wrap("1;31", t) }

var (
	colonHead = regexp.MustCompile(`^([A-Za-z][\w-]*:)(\s.*)?$`) // "usage: ...", "env:  ..."
	gapHead   = regexp.MustCompile(`^(\S+)(\s{2,}.*)$`)          // "VIEW      cat ..."
	listFlag  = regexp.MustCompile(`^(\s+)(-[A-Za-z])\b(.*)$`)   // "  -c 'SCRIPT'  ..."
	inline    = regexp.MustCompile(`(^|[\s\[(|])(--[A-Za-z][\w-]*)|(\$[A-Z]+\b|\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b)`)
)

// Help styles the manual's layout: the first line is the title, words in the
// first column are section labels, and long flags, a short flag opening an
// indented line, and environment variables are highlighted; short flags in the
// dense command reference stay plain. Stripping the escapes gives back the text exactly.
func (s Styler) Help(text string) string {
	if !s.On {
		return text
	}
	var b strings.Builder
	for i, line := range strings.SplitAfter(text, "\n") {
		body := strings.TrimSuffix(line, "\n")
		nl := line[len(body):]
		switch m := colonHead.FindStringSubmatch(body); {
		case body == "":
		case i == 0:
			body = s.Bold(body)
		case m != nil:
			body = s.Bold(m[1]) + s.inline(m[2])
		case gapHead.MatchString(body):
			m := gapHead.FindStringSubmatch(body)
			body = s.Bold(m[1]) + s.inline(m[2])
		case listFlag.MatchString(body):
			m := listFlag.FindStringSubmatch(body)
			body = m[1] + s.Cyan(m[2]) + s.inline(m[3])
		case body[0] != ' ' && len(body) <= 30 && !strings.HasSuffix(body, "."):
			body = s.Bold(body) // a heading on its own line: "Examples", "Hosts"
		default:
			body = s.inline(body)
		}
		b.WriteString(body + nl)
	}
	return b.String()
}

func (s Styler) inline(t string) string {
	var b strings.Builder
	last := 0
	for _, m := range inline.FindAllStringSubmatchIndex(t, -1) {
		if m[4] >= 0 { // flag, after its delimiter
			b.WriteString(t[last:m[4]])
			b.WriteString(s.Cyan(t[m[4]:m[5]]))
			last = m[5]
		} else {
			b.WriteString(t[last:m[6]])
			b.WriteString(s.Yellow(t[m[6]:m[7]]))
			last = m[7]
		}
	}
	return b.String() + t[last:]
}

var escape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Strip removes the escapes Styler adds.
func Strip(t string) string { return escape.ReplaceAllString(t, "") }
