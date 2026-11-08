package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Fallback opens Primary and, if a session cannot be established, retries with
// Secondary. It is used to fall back from ssh to anonymous HTTPS when ssh is
// unusable (no key, port 22 blocked, ssh not installed). A failed Secondary is
// reported alongside Primary's error so the real cause is never masked.
type Fallback struct {
	Primary    Transport
	Secondary  Transport
	OnFallback func()
	Logf       func(format string, args ...any)
}

// maxAdvertisementBytes mirrors the protocol client's cap on the capability
// advertisement that probe buffers.
const maxAdvertisementBytes = 1 << 20

func (f Fallback) Open(ctx context.Context) (Conn, error) {
	conn, err := probe(ctx, f.Primary)
	if err == nil {
		return conn, nil
	}
	if f.Logf != nil {
		f.Logf("primary transport failed: %v; trying fallback", err)
	}
	fconn, ferr := probe(ctx, f.Secondary)
	if ferr != nil {
		return nil, fmt.Errorf("%w (fallback also failed: %v)", err, ferr)
	}
	if f.OnFallback != nil {
		f.OnFallback()
	}
	return fconn, nil
}

// probe opens t and reads the protocol v2 capability advertisement through its
// terminating flush-pkt. Doing the handshake here makes a broken session fail
// at Open time, where Fallback can react, instead of on the caller's first
// read. The returned Conn replays the advertisement before the live stream.
func probe(ctx context.Context, t Transport) (Conn, error) {
	conn, err := t.Open(ctx)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	buf, err := readCaps(conn)
	close(done)
	if err != nil {
		se := strings.TrimSpace(conn.Stderr())
		conn.Close()
		if se != "" {
			return nil, errors.New(se)
		}
		return nil, err
	}
	return &peekConn{Conn: conn, buf: buf}, nil
}

// peekConn serves buffered bytes before delegating to the wrapped Conn.
type peekConn struct {
	Conn
	buf []byte
}

func (p *peekConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// readCaps reads pkt-lines up to and including the first flush, returning the
// raw bytes. The v2 capability advertisement is exactly that sequence.
func readCaps(r io.Reader) ([]byte, error) {
	var buf []byte
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return buf, err
		}
		n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
		if err != nil {
			return buf, fmt.Errorf("malformed pkt-line length %q", hdr[:])
		}
		buf = append(buf, hdr[:]...)
		switch {
		case n == 0, n == 1, n == 2: // flush, delim or response-end: no payload
			return buf, nil
		case n == 3:
			return buf, errors.New("malformed pkt-line: length 3")
		case n > 65524:
			return buf, fmt.Errorf("pkt-line too long (%d)", n)
		}
		payload := make([]byte, n-4)
		if _, err := io.ReadFull(r, payload); err != nil {
			return buf, err
		}
		buf = append(buf, payload...)
		if len(buf) > maxAdvertisementBytes {
			return buf, fmt.Errorf("server sent an oversized capability advertisement (over %d bytes)", maxAdvertisementBytes)
		}
	}
}
