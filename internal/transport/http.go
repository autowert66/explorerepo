package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// userAgent identifies the client to the server. Git itself sends "git/<ver>".
const userAgent = "git/2.56.0"

// HTTP talks git protocol v2 to a remote upload-pack over smart HTTP. It is
// anonymous by design: no credentials, no credential helpers, no prompts, and
// no GitHub API. Public repositories are readable this way; private ones are
// not, and still need ssh.
type HTTP struct {
	Scheme string // "https" (default) or "http"
	Host   string // host[:port]
	Path   string // repository path, e.g. /owner/repo.git
	Client *http.Client
}

func (h HTTP) scheme() string {
	if h.Scheme != "" {
		return h.Scheme
	}
	return "https"
}

func (h HTTP) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

// base is the repository URL without a trailing slash.
func (h HTTP) base() string {
	return h.scheme() + "://" + h.Host + "/" + strings.TrimPrefix(h.Path, "/")
}

// statusError describes a non-200 response. Anonymous HTTPS cannot read private
// repositories, which hosts report as 401 or (to avoid leaking existence) 404.
func statusError(base string, resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%s: HTTP %s (repository not found, or private: anonymous HTTPS cannot read private repositories)",
			base, resp.Status)
	}
	return fmt.Errorf("%s: HTTP %s", base, resp.Status)
}

// Open fetches the capability advertisement; the returned Conn then posts one
// request per flush-terminated command, as stateless smart HTTP requires.
// Repository specs get a ".git" suffix, which most hosts accept; some (e.g.
// sourcehut) only serve the bare name, so a 404 is retried without it.
func (h HTTP) Open(ctx context.Context) (Conn, error) {
	c, status, err := h.open(ctx)
	if bare, ok := strings.CutSuffix(h.Path, ".git"); ok && status == http.StatusNotFound {
		h.Path = bare
		if c2, _, err2 := h.open(ctx); err2 == nil {
			return c2, nil
		}
	}
	return c, err
}

// open is Open for one path; status is the info/refs HTTP status, or 0.
func (h HTTP) open(ctx context.Context) (_ Conn, status int, _ error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base()+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/x-git-upload-pack-advertisement")
	resp, err := h.client().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", h.base(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, statusError(h.base(), resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	caps, err := stripService(body)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", h.base(), err)
	}
	return &httpConn{ctx: ctx, h: h, client: h.client(), caps: caps}, http.StatusOK, nil
}

// stripService removes the "# service=git-upload-pack" packet and the flush
// that follows it from the info/refs response, leaving the v2 capability
// advertisement. The announcement is optional in v2: git http-backend (as on
// kernel.org and googlesource) starts with "version 2" directly, as over ssh.
func stripService(b []byte) ([]byte, error) {
	r := bytes.NewReader(b)
	payload, flush, err := readPkt(r)
	if err != nil {
		return nil, err
	}
	if !flush && bytes.HasPrefix(payload, []byte("version 2")) {
		return b, nil
	}
	if flush || !bytes.HasPrefix(payload, []byte("# service=")) {
		return nil, fmt.Errorf("unexpected first packet %q", payload)
	}
	if _, flush, err = readPkt(r); err != nil {
		return nil, err
	}
	if !flush {
		return nil, errors.New("missing flush after service announcement")
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return rest, nil
}

// readPkt reads one pkt-line from r. flush reports a flush packet; the other
// empty packets (delim, response-end) are reported as neither.
func readPkt(r *bytes.Reader) (payload []byte, flush bool, err error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, false, err
	}
	n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
	if err != nil {
		return nil, false, fmt.Errorf("malformed pkt-line length %q", hdr[:])
	}
	switch {
	case n == 0:
		return nil, true, nil
	case n == 1, n == 2:
		return nil, false, nil
	case n == 3, n > 65524:
		return nil, false, fmt.Errorf("malformed pkt-line length %d", n)
	}
	p := make([]byte, n-4)
	if _, err := io.ReadFull(r, p); err != nil {
		return nil, false, err
	}
	return p, false, nil
}

type httpConn struct {
	ctx    context.Context
	h      HTTP
	client *http.Client

	mu     sync.Mutex
	caps   []byte
	req    []byte
	resp   io.ReadCloser
	closed bool
}

func (c *httpConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if len(c.caps) > 0 {
		n := copy(p, c.caps)
		c.caps = c.caps[n:]
		c.mu.Unlock()
		return n, nil
	}
	resp := c.resp
	c.mu.Unlock()
	if resp == nil {
		return 0, io.EOF
	}
	n, err := resp.Read(p)
	if err == io.EOF {
		c.mu.Lock()
		if c.resp == resp {
			c.resp = nil
		}
		c.mu.Unlock()
		resp.Close()
	}
	return n, err
}

// Write buffers request packets and, once the command's terminating flush-pkt
// arrives, posts them and makes the response the read stream.
func (c *httpConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.req = append(c.req, p...)
	if !endsWithFlush(c.req) {
		c.mu.Unlock()
		return len(p), nil
	}
	body := c.req
	c.req = nil
	c.mu.Unlock()
	if err := c.post(body); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *httpConn) post(body []byte) error {
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.h.base()+"/git-upload-pack", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("Accept", "application/x-git-upload-pack-result")
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.h.base(), err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if msg := strings.TrimSpace(string(b)); msg != "" {
			return fmt.Errorf("%s: HTTP %s: %s", c.h.base(), resp.Status, msg)
		}
		return statusError(c.h.base(), resp)
	}
	c.mu.Lock()
	if c.resp != nil {
		c.resp.Close()
	}
	c.resp = resp.Body
	c.mu.Unlock()
	return nil
}

// CloseWrite posts any request still buffered without its terminating flush.
func (c *httpConn) CloseWrite() error {
	c.mu.Lock()
	body := c.req
	c.req = nil
	c.mu.Unlock()
	if len(body) == 0 {
		return nil
	}
	return c.post(body)
}

func (c *httpConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.resp != nil {
		c.resp.Close()
		c.resp = nil
	}
	return nil
}

func (c *httpConn) Stderr() string { return "" }

// endsWithFlush reports whether b is a whole number of pkt-lines ending in a
// flush-pkt. The v2 client terminates every command with a flush, so this marks
// the boundary at which smart HTTP must issue a POST.
func endsWithFlush(b []byte) bool {
	for i := 0; i+4 <= len(b); {
		n, err := strconv.ParseUint(string(b[i:i+4]), 16, 16)
		if err != nil {
			return false
		}
		switch {
		case n == 0:
			return i+4 == len(b)
		case n == 1, n == 2:
			i += 4
		case n == 3, n > 65524, i+int(n) > len(b):
			return false
		default:
			i += int(n)
		}
	}
	return false
}
