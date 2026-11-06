package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

var testAdvert = pkt("version 2\n") +
	pkt("fetch=shallow wait-for-done filter\n") +
	pkt("object-format=sha1\n") +
	"0000"

type testGitServer struct {
	srv    *httptest.Server
	advert string

	mu     sync.Mutex
	posted []byte
}

func newTestGitServer(t *testing.T, advert string) *testGitServer {
	t.Helper()
	ts := &testGitServer{advert: advert}
	mux := http.NewServeMux()
	mux.HandleFunc("/o/r.git/info/refs", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("service"); got != "git-upload-pack" {
			t.Errorf("service = %q", got)
		}
		if r.Header.Get("Git-Protocol") != "version=2" {
			t.Errorf("missing Git-Protocol header")
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		io.WriteString(w, pkt("# service=git-upload-pack\n")+"0000"+ts.advert)
	})
	mux.HandleFunc("/o/r.git/git-upload-pack", func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-git-upload-pack-request" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if !endsWithFlush(body) {
			t.Errorf("request does not end with a flush-pkt: %q", body)
		}
		ts.mu.Lock()
		ts.posted = body
		ts.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		io.WriteString(w, pkt("HEAD\n")+"0000")
	})
	ts.srv = httptest.NewServer(mux)
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *testGitServer) host() string { return strings.TrimPrefix(ts.srv.URL, "http://") }

func (ts *testGitServer) lastPosted() []byte {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.posted
}

func TestHTTPOpenPostAndCaps(t *testing.T) {
	ts := newTestGitServer(t, testAdvert)
	conn, err := HTTP{Scheme: "http", Host: ts.host(), Path: "/o/r.git"}.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	caps, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(caps) != testAdvert {
		t.Fatalf("caps = %q, want %q", caps, testAdvert)
	}

	req := pkt("command=ls-refs\n") + "0001" + pkt("peel\n") + pkt("symrefs\n") + "0000"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	resp, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(resp, []byte("HEAD")) {
		t.Fatalf("response = %q", resp)
	}
	if got := string(ts.lastPosted()); got != req {
		t.Fatalf("posted = %q, want %q", got, req)
	}
}

func TestHTTPPostErrorStatus(t *testing.T) {
	ts := newTestGitServer(t, testAdvert)
	conn, err := HTTP{Scheme: "http", Host: ts.host(), Path: "/o/r.git"}.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatal(err)
	}
	// A request that never ends in a flush must not be posted.
	if _, err := conn.Write([]byte(pkt("command=fetch\n"))); err != nil {
		t.Fatal(err)
	}
	if got := ts.lastPosted(); got != nil {
		t.Fatalf("posted incomplete request: %q", got)
	}
}

func TestStripService(t *testing.T) {
	body := []byte(pkt("# service=git-upload-pack\n") + "0000" + testAdvert)
	got, err := stripService(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != testAdvert {
		t.Fatalf("got %q, want %q", got, testAdvert)
	}
	// git http-backend omits the announcement under v2.
	if got, err := stripService([]byte(testAdvert)); err != nil || string(got) != testAdvert {
		t.Fatalf("no announcement: got %q, %v", got, err)
	}
	if _, err := stripService([]byte(pkt("hello\n"))); err == nil {
		t.Fatal("expected error for an unknown first packet")
	}
}

func TestHTTPRetriesWithoutGitSuffix(t *testing.T) {
	ts := newTestGitServer(t, testAdvert)
	// The test server serves /o/r.git; ask for /o/r.git.git, which 404s.
	h := HTTP{Scheme: "http", Host: ts.host(), Path: "/o/r.git.git"}
	conn, err := h.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	h.Path = "/o/missing.git"
	if _, err := h.Open(context.Background()); err == nil || !strings.Contains(err.Error(), "missing.git") {
		t.Fatalf("want the original 404 for the suffixed path, got %v", err)
	}
}

func TestEndsWithFlush(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"0000", true},
		{pkt("hi\n") + "0000", true},
		{pkt("hi\n"), false},
		{pkt("command=fetch\n") + "0001" + pkt("want x\n") + "0000", true},
		{pkt("hi\n") + "0000" + pkt("more\n"), false},
		{"0003", false},
		{pkt("a\n") + "000", false},
	}
	for _, c := range cases {
		if got := endsWithFlush([]byte(c.in)); got != c.want {
			t.Errorf("endsWithFlush(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// bufConn is a minimal Conn over an in-memory advertisement.
type bufConn struct{ *bytes.Reader }

func (b *bufConn) Write(p []byte) (int, error) { return len(p), nil }
func (b *bufConn) CloseWrite() error           { return nil }
func (b *bufConn) Close() error                { return nil }
func (b *bufConn) Stderr() string              { return "" }

type stubTransport struct {
	conn Conn
	err  error
}

func (s stubTransport) Open(context.Context) (Conn, error) { return s.conn, s.err }

func TestFallbackPrimaryWins(t *testing.T) {
	called := false
	f := Fallback{
		Primary:    stubTransport{conn: &bufConn{bytes.NewReader([]byte(testAdvert))}},
		Secondary:  stubTransport{err: errors.New("secondary must not be used")},
		OnFallback: func() { called = true },
	}
	conn, err := f.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	b, _ := io.ReadAll(conn)
	if string(b) != testAdvert {
		t.Fatalf("caps = %q", b)
	}
	if called {
		t.Fatal("OnFallback called although the primary succeeded")
	}
}

func TestFallbackUsesSecondary(t *testing.T) {
	ts := newTestGitServer(t, testAdvert)
	called := false
	f := Fallback{
		Primary:    stubTransport{err: errors.New("ssh: permission denied (publickey)")},
		Secondary:  HTTP{Scheme: "http", Host: ts.host(), Path: "/o/r.git"},
		OnFallback: func() { called = true },
	}
	conn, err := f.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	b, _ := io.ReadAll(conn)
	if string(b) != testAdvert {
		t.Fatalf("caps = %q", b)
	}
	if !called {
		t.Fatal("OnFallback not called")
	}
}

func TestFallbackReportsBothErrors(t *testing.T) {
	f := Fallback{
		Primary:   stubTransport{err: errors.New("ssh: no key")},
		Secondary: stubTransport{err: errors.New("https: HTTP 404")},
	}
	_, err := f.Open(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "ssh: no key") || !strings.Contains(err.Error(), "https: HTTP 404") {
		t.Fatalf("err = %v", err)
	}
}
