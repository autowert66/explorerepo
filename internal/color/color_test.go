package color

import (
	"bytes"
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	for _, k := range append([]string{"NO_COLOR", "CLICOLOR_FORCE", "FORCE_COLOR", "TERM"}, agentEnv...) {
		t.Setenv(k, "")
	}
}

func TestEnabled(t *testing.T) {
	var buf bytes.Buffer
	for _, c := range []struct {
		env  map[string]string
		mode string
		want bool
	}{
		{nil, Auto, false}, // not a terminal
		{nil, Always, true},
		{map[string]string{"FORCE_COLOR": "1"}, Auto, true},
		{map[string]string{"FORCE_COLOR": "0"}, Auto, false},
		{map[string]string{"CLICOLOR_FORCE": "1", "CLAUDECODE": "1"}, Auto, true},
		{map[string]string{"CLICOLOR_FORCE": "1", "NO_COLOR": "1"}, Auto, false},
		{map[string]string{"FORCE_COLOR": "1"}, Never, false},
		{map[string]string{"NO_COLOR": "1"}, Always, true},
	} {
		clearEnv(t)
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		if got := Enabled(&buf, c.mode); got != c.want {
			t.Errorf("Enabled(%v, %s) = %v, want %v", c.env, c.mode, got, c.want)
		}
	}
	if _, err := ParseMode("yes"); err == nil {
		t.Error("ParseMode(yes) accepted")
	}
}

func TestHelp(t *testing.T) {
	text := "tool: a title line\n\nusage: tool --repo SPEC\nVIEW      cat [-n -b] FILE   wc\n  -c 'SCRIPT'   run it; also -h, --help\nExamples\n  ls -la; echo $REPO\nPaths are read-only - see EXPLOREREPO_CACHE.\n"
	s := Styler{On: true}
	got := s.Help(text)
	if Strip(got) != text {
		t.Fatalf("stripped help differs:\n%q\n%q", Strip(got), text)
	}
	for _, want := range []string{
		s.Bold("tool: a title line"), s.Bold("usage:"), s.Cyan("--repo"), s.Bold("VIEW"),
		s.Bold("Examples"), "  " + s.Cyan("-c") + " ", s.Cyan("--help"), s.Yellow("$REPO"), s.Yellow("EXPLOREREPO_CACHE"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("help lacks %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, s.Cyan("-only")) || strings.Contains(got, s.Cyan("-n")) || strings.Contains(got, s.Cyan("-h")) || strings.Contains(got, s.Bold("Paths")) {
		t.Errorf("prose styled: %q", got)
	}
	if (Styler{}).Help(text) != text {
		t.Error("Styler off changed the text")
	}
}
