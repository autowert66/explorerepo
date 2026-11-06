// Package transport opens byte streams to a remote git-upload-pack.
package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Conn is a bidirectional stream to an upload-pack process.
type Conn interface {
	io.ReadWriteCloser
	CloseWrite() error
	// Stderr returns whatever the remote side / ssh wrote to stderr so far.
	Stderr() string
}

type Transport interface {
	Open(ctx context.Context) (Conn, error)
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.b.Len() < 64*1024 {
		s.b.Write(p)
	}
	return len(p), nil
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type procConn struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	stderr *safeBuf
	once   sync.Once
	done   chan struct{}
}

func startProc(cmd *exec.Cmd) (Conn, error) {
	c := &procConn{cmd: cmd, stderr: &safeBuf{}, done: make(chan struct{})}
	// Give the child a pipe we own instead of letting os/exec copy stderr:
	// Wait() would otherwise block until *every* holder of the pipe exits, and
	// an ssh ControlPersist master forked from this client inherits it.
	errR, errW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = errW
	if c.in, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if c.out, err = cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		errR.Close()
		errW.Close()
		return nil, fmt.Errorf("starting %s: %w", filepath.Base(cmd.Path), err)
	}
	errW.Close()
	go func() { io.Copy(c.stderr, errR); errR.Close() }()
	go func() { cmd.Wait(); close(c.done) }()
	return c, nil
}

func (c *procConn) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *procConn) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *procConn) CloseWrite() error           { return c.in.Close() }
func (c *procConn) Stderr() string              { return c.stderr.String() }

func (c *procConn) Close() error {
	c.once.Do(func() {
		c.in.Close()
		select {
		case <-c.done:
		case <-time.After(500 * time.Millisecond):
			c.cmd.Process.Kill()
			select {
			case <-c.done:
			case <-time.After(2 * time.Second):
			}
		}
	})
	return nil
}

// Local runs the local git-upload-pack against a repository path.
type Local struct{ Path string }

func (l Local) Open(ctx context.Context) (Conn, error) {
	cmd := exec.Command("git", "upload-pack", "--strict", l.Path)
	cmd.Env = append(os.Environ(), "GIT_PROTOCOL=version=2")
	return startProc(cmd)
}

// SSH runs git-upload-pack on a remote host through the system ssh binary.
type SSH struct {
	User, Host string
	Port       int
	Path       string // remote repository path, unquoted
	NoMux      bool
}

// MuxDir returns the private directory used for ssh control sockets. It must
// be short: unix socket paths are limited to ~104 bytes on macOS.
func MuxDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "explorerepo-"+strconv.Itoa(os.Getuid()))
	if len(dir) > 40 {
		dir = filepath.Join("/tmp", "explorerepo-"+strconv.Itoa(os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func (s SSH) Args() ([]string, error) {
	args := []string{"-o", "SendEnv=GIT_PROTOCOL", "-o", "BatchMode=yes"}
	// Always set ControlPath explicitly: a user-configured ControlMaster that
	// was started without SendEnv=GIT_PROTOCOL silently drops the variable
	// and the server falls back to protocol v0.
	if s.NoMux {
		args = append(args, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	} else {
		dir, err := MuxDir()
		if err != nil {
			return nil, err
		}
		args = append(args, "-o", "ControlMaster=auto", "-o", "ControlPath="+dir+"/%C", "-o", "ControlPersist=300")
	}
	if s.Port != 0 {
		args = append(args, "-p", strconv.Itoa(s.Port))
	}
	target := s.Host
	if s.User != "" {
		target = s.User + "@" + s.Host
	}
	args = append(args, "--", target, "git-upload-pack '"+s.Path+"'")
	return args, nil
}

func (s SSH) Open(ctx context.Context) (Conn, error) {
	args, err := s.Args()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("ssh", args...)
	cmd.Env = append(os.Environ(), "GIT_PROTOCOL=version=2")
	return startProc(cmd)
}

// Counting wraps a Transport and records usage; used by tests to assert batching.
type Counting struct {
	Inner    Transport
	sessions atomic.Int64
	bytes    atomic.Int64
	mu       sync.Mutex
	reqs     map[string]int
	log      []string
}

func NewCounting(inner Transport) *Counting { return &Counting{Inner: inner, reqs: map[string]int{}} }

func (c *Counting) Open(ctx context.Context) (Conn, error) {
	conn, err := c.Inner.Open(ctx)
	if err != nil {
		return nil, err
	}
	c.sessions.Add(1)
	return &countingConn{Conn: conn, c: c}, nil
}

func (c *Counting) Sessions() int    { return int(c.sessions.Load()) }
func (c *Counting) BytesRead() int64 { return c.bytes.Load() }

// Requests returns the number of requests issued for a command ("ls-refs", "fetch", ...).
func (c *Counting) Requests(cmd string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reqs[cmd]
}

// Log returns request descriptions in order.
func (c *Counting) Log() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.log...)
}

// Reset zeroes request counters (not sessions).
func (c *Counting) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = map[string]int{}
	c.log = nil
	c.sessions.Store(0)
	c.bytes.Store(0)
}

type countingConn struct {
	Conn
	c *Counting
}

func (cc *countingConn) Read(p []byte) (int, error) {
	n, err := cc.Conn.Read(p)
	cc.c.bytes.Add(int64(n))
	return n, err
}

// NoteRequest is called by the protocol client for each command it sends.
func (cc *countingConn) NoteRequest(cmd, desc string) {
	cc.c.mu.Lock()
	cc.c.reqs[cmd]++
	cc.c.log = append(cc.c.log, desc)
	cc.c.mu.Unlock()
}

// RequestNoter is implemented by Conns that want to observe requests.
type RequestNoter interface{ NoteRequest(cmd, desc string) }
