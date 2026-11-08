package transport

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/autowert66/explorerepo/internal/pktline"
	"github.com/autowert66/explorerepo/internal/testutil"
)

// The fallback probe buffers the whole advertisement, so it needs a bound too.
func TestReadCapsRejectsOversizedAdvertisement(t *testing.T) {
	var buf bytes.Buffer
	w := pktline.NewWriter(&buf)
	for i := 0; i < 40_000; i++ {
		if err := w.Line("k" + strconv.Itoa(i) + "=" + strings.Repeat("x", 100)); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Flush()

	if _, err := readCaps(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected an error for an oversized capability advertisement")
	}
}

// TestLocalNonBare guards against --strict, which refuses a normal checkout by
// disabling the <dir>/.git lookup. The bare-only test fixtures never caught it.
func TestLocalNonBare(t *testing.T) {
	f := testutil.NewFixture(t)
	conn, err := Local{Path: f.Work}.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// First pkt-line of the v2 advertisement is "version 2\n".
	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		t.Fatalf("reading length: %v (stderr: %s)", err, conn.Stderr())
	}
	n, err := hex.DecodeString(string(lenBuf[:]))
	if err != nil || len(n) != 2 {
		t.Fatalf("bad pkt length %q", lenBuf[:])
	}
	size := int(n[0])<<8 | int(n[1])
	if size < 4 || size > 65520 {
		t.Fatalf("bad pkt size %d", size)
	}
	payload := make([]byte, size-4)
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Fatalf("reading payload: %v (stderr: %s)", err, conn.Stderr())
	}
	if !strings.HasPrefix(string(payload), "version 2") {
		t.Fatalf("first pkt = %q, want version 2 (stderr: %s)", payload, conn.Stderr())
	}
}

func TestSSHArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenSSH for Windows has no control sockets; see TestSSHArgsNoMuxAndPort")
	}
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
