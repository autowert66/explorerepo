package transport

import (
	"strings"
	"testing"
)

func TestSSHArgs(t *testing.T) {
	args, err := SSH{User: "git", Host: "github.com", Path: "o/r.git"}.Args()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"SendEnv=GIT_PROTOCOL", "BatchMode=yes", "ControlMaster=auto", "ControlPersist=300",
		"-- git@github.com git-upload-pack 'o/r.git'",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	// a private ControlPath is mandatory: a user's own mux may drop GIT_PROTOCOL
	var cp string
	for _, a := range args {
		if strings.HasPrefix(a, "ControlPath=") {
			cp = strings.TrimPrefix(a, "ControlPath=")
		}
	}
	if !strings.Contains(cp, "explorerepo-") || !strings.HasSuffix(cp, "/%C") {
		t.Errorf("ControlPath = %q", cp)
	}
	if len(cp) > 70 {
		t.Errorf("ControlPath too long for unix sockets on macOS: %d", len(cp))
	}
}

func TestSSHArgsNoMuxAndPort(t *testing.T) {
	args, _ := SSH{User: "me", Host: "h", Port: 2222, Path: "/srv/r.git", NoMux: true}.Args()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "ControlPath=none") || strings.Contains(joined, "ControlMaster=auto") {
		t.Errorf("no-mux args: %s", joined)
	}
	if !strings.Contains(joined, "-p 2222") || !strings.Contains(joined, "me@h") {
		t.Errorf("args: %s", joined)
	}
}
