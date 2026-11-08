// Package protocol implements the client side of git wire protocol v2.
package protocol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/pack"
	"github.com/autowert66/explorerepo/internal/pktline"
	"github.com/autowert66/explorerepo/internal/transport"
)

type Caps struct {
	Raw          map[string]string
	Fetch        map[string]bool
	ObjectInfo   bool
	ObjectFormat string
}

type Ref struct {
	Name         string
	OID          object.OID
	Peeled       object.OID // zero if not an annotated tag
	SymrefTarget string
}

// ErrBudget is returned when a fetch exceeds its byte budget.
var ErrBudget = errors.New("fetch budget exceeded")

const (
	// maxAdvertisementBytes bounds the v2 capability advertisement. The HTTP
	// transport reads at most this much from info/refs as well.
	maxAdvertisementBytes = 1 << 20
	// maxListBytes bounds the bytes of a ref listing or object-info response.
	maxListBytes = 8 << 20
)

type Client struct {
	conn   transport.Conn
	pr     *pktline.Reader
	pw     *pktline.Writer
	Caps   Caps
	Logf   func(format string, args ...any)
	mu     sync.Mutex
	broken atomic.Bool
}

func (c *Client) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

// Dial opens a session and reads the capability advertisement.
func Dial(ctx context.Context, t transport.Transport) (*Client, error) {
	conn, err := t.Open(ctx)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, pr: pktline.NewReader(conn), pw: pktline.NewWriter(conn)}
	if err := c.readCaps(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) wrapErr(err error) error {
	if err == nil {
		return nil
	}
	c.broken.Store(true)
	if se := strings.TrimSpace(c.conn.Stderr()); se != "" {
		return fmt.Errorf("%s", se)
	}
	return err
}

// watch aborts blocking I/O when ctx is cancelled; call the returned func when done.
func (c *Client) watch(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.broken.Store(true)
			c.conn.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func (c *Client) readCaps(ctx context.Context) error {
	defer c.watch(ctx)()
	k, p, err := c.pr.ReadPkt()
	if err != nil {
		if se := strings.TrimSpace(c.conn.Stderr()); se != "" {
			return errors.New(se)
		}
		if err == io.EOF {
			err = errors.New("connection closed by remote")
		}
		return err
	}
	first := strings.TrimRight(string(p), "\n")
	if k != pktline.Data || first != "version 2" {
		if strings.HasPrefix(first, "ERR ") {
			return errors.New(first[4:])
		}
		return fmt.Errorf("server did not speak protocol v2 (got a v0 ref advertisement; "+
			"with ssh, check that a ControlMaster is not dropping GIT_PROTOCOL): %.60q", first)
	}
	c.Caps = Caps{Raw: map[string]string{}, Fetch: map[string]bool{}}
	total := len(p) + 4
	for {
		k, p, err := c.pr.ReadPkt()
		if err != nil {
			return c.wrapErr(err)
		}
		if k == pktline.Flush {
			break
		}
		if k != pktline.Data {
			continue
		}
		total += len(p) + 4
		if total > maxAdvertisementBytes {
			return fmt.Errorf("server sent an oversized capability advertisement (over %d bytes)", maxAdvertisementBytes)
		}
		key, val, _ := strings.Cut(strings.TrimRight(string(p), "\n"), "=")
		c.Caps.Raw[key] = val
		switch key {
		case "fetch":
			for _, f := range strings.Fields(val) {
				c.Caps.Fetch[f] = true
			}
		case "object-info":
			c.Caps.ObjectInfo = true
		case "object-format":
			c.Caps.ObjectFormat = val
		}
	}
	if _, ok := c.Caps.Raw["fetch"]; !ok {
		return errors.New("server does not support the v2 fetch command")
	}
	if !c.Caps.Fetch["filter"] {
		return errors.New("server does not support filtered fetch; explorerepo cannot work without cloning")
	}
	if f := c.Caps.ObjectFormat; f != "" && f != "sha1" {
		return fmt.Errorf("unsupported object format %q (only sha1)", f)
	}
	return nil
}

func (c *Client) Broken() bool { return c.broken.Load() }

func (c *Client) Close() error { return c.conn.Close() }

// begin writes the command header and arguments.
func (c *Client) begin(name, desc string, args []string) error {
	if n, ok := c.conn.(transport.RequestNoter); ok {
		n.NoteRequest(name, desc)
	}
	c.logf("request %s", desc)
	if err := c.pw.Line("command=" + name); err != nil {
		return err
	}
	if _, ok := c.Caps.Raw["object-format"]; ok {
		if err := c.pw.Line("object-format=sha1"); err != nil {
			return err
		}
	}
	if err := c.pw.Delim(); err != nil {
		return err
	}
	for _, a := range args {
		if err := c.pw.Line(a); err != nil {
			return err
		}
	}
	return c.pw.Flush()
}

func (c *Client) LsRefs(ctx context.Context, prefixes []string) ([]Ref, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken.Load() {
		return nil, errors.New("session closed")
	}
	defer c.watch(ctx)()
	args := []string{"peel", "symrefs"}
	for _, p := range prefixes {
		args = append(args, "ref-prefix "+p)
	}
	if err := c.begin("ls-refs", "ls-refs "+strings.Join(prefixes, ","), args); err != nil {
		return nil, c.wrapErr(err)
	}
	var refs []Ref
	total := 0
	for {
		k, p, err := c.pr.ReadPkt()
		if err != nil {
			return nil, c.wrapErr(err)
		}
		if k == pktline.Flush {
			return refs, nil
		}
		if k != pktline.Data {
			continue
		}
		total += len(p) + 4
		if total > maxListBytes {
			return nil, fmt.Errorf("server sent an oversized ref listing (over %d bytes)", maxListBytes)
		}
		line := strings.TrimRight(string(p), "\n")
		if strings.HasPrefix(line, "ERR ") {
			return nil, errors.New(line[4:])
		}
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "unborn" {
			continue
		}
		oid, err := object.ParseOID(f[0])
		if err != nil {
			continue
		}
		r := Ref{Name: f[1], OID: oid}
		for _, attr := range f[2:] {
			if v, ok := strings.CutPrefix(attr, "symref-target:"); ok {
				r.SymrefTarget = v
			} else if v, ok := strings.CutPrefix(attr, "peeled:"); ok {
				r.Peeled, _ = object.ParseOID(v)
			}
		}
		refs = append(refs, r)
	}
}

type FetchReq struct {
	Wants    []object.OID
	Filter   string
	Deepen   int
	MaxBytes int64 // abort once this many pack bytes were received (0 = unlimited)
	Base     func(object.OID) (object.Type, []byte, bool)
}

// FetchResult describes a completed fetch.
type FetchResult struct {
	PackBytes int64
	Objects   int
}

// Fetch requests objects and streams every resolved object to sink.
func (c *Client) Fetch(ctx context.Context, req FetchReq, sink func(pack.Raw) error) (FetchResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var res FetchResult
	if c.broken.Load() {
		return res, errors.New("session closed")
	}
	defer c.watch(ctx)()

	args := []string{"ofs-delta", "no-progress"}
	if req.Deepen > 0 {
		args = append(args, "deepen "+strconv.Itoa(req.Deepen))
	}
	if req.Filter != "" {
		args = append(args, "filter "+req.Filter)
	}
	for _, w := range req.Wants {
		args = append(args, "want "+w.String())
	}
	args = append(args, "done")
	desc := fmt.Sprintf("fetch wants=%d filter=%q deepen=%d", len(req.Wants), req.Filter, req.Deepen)
	if err := c.begin("fetch", desc, args); err != nil {
		return res, c.wrapErr(err)
	}

	var (
		pw        *io.PipeWriter
		parseErr  = make(chan error, 1)
		section   string
		ctrlBytes int
	)
	startParser := func() {
		pr, w := io.Pipe()
		pw = w
		popt := pack.Options{Base: req.Base}
		if req.MaxBytes > 0 {
			// The byte budget counts compressed pack bytes; cap the
			// decompressed side too, per object and in total.
			popt.MaxObjectSize = req.MaxBytes
			popt.MaxTotalBytes = req.MaxBytes
		}
		go func() {
			err := pack.Parse(pr, popt, func(r pack.Raw) error {
				res.Objects++
				return sink(r)
			})
			pr.CloseWithError(errors.New("parser stopped"))
			parseErr <- err
		}()
	}
	fail := func(err error) (FetchResult, error) {
		c.broken.Store(true)
		if pw != nil {
			pw.CloseWithError(err)
			<-parseErr
		}
		return res, err
	}

	for {
		k, p, err := c.pr.ReadPkt()
		if err != nil {
			return fail(c.wrapErr(err))
		}
		if k == pktline.Flush {
			break
		}
		if k == pktline.Delim {
			continue
		}
		if section == "packfile" {
			if len(p) == 0 {
				continue
			}
			switch p[0] {
			case 1:
				res.PackBytes += int64(len(p) - 1)
				if req.MaxBytes > 0 && res.PackBytes > req.MaxBytes {
					return fail(ErrBudget)
				}
				if _, err := pw.Write(p[1:]); err != nil {
					perr := <-parseErr
					pw = nil
					c.broken.Store(true)
					if perr == nil {
						perr = err
					}
					return res, perr
				}
			case 2:
				c.logf("remote: %s", bytes.TrimSpace(p[1:]))
			case 3:
				return fail(fmt.Errorf("remote error: %s", bytes.TrimSpace(p[1:])))
			default:
				return fail(fmt.Errorf("unknown sideband channel %d", p[0]))
			}
			continue
		}
		ctrlBytes += len(p) + 4
		if ctrlBytes > maxAdvertisementBytes {
			return fail(errors.New("server sent an oversized fetch control section"))
		}
		line := strings.TrimRight(string(p), "\n")
		if strings.HasPrefix(line, "ERR ") {
			return fail(errors.New(line[4:]))
		}
		switch line {
		case "packfile":
			section = "packfile"
			startParser()
		case "acknowledgments", "shallow-info", "wanted-refs", "packfile-uris":
			section = line
		}
	}
	if pw == nil {
		return res, errors.New("server sent no packfile")
	}
	pw.Close()
	if err := <-parseErr; err != nil {
		c.broken.Store(true)
		return res, err
	}
	c.logf("fetched %d objects, %d pack bytes", res.Objects, res.PackBytes)
	return res, nil
}

// ObjectInfo returns object sizes; only valid if Caps.ObjectInfo.
func (c *Client) ObjectInfo(ctx context.Context, oids []object.OID) (map[object.OID]int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.Caps.ObjectInfo {
		return nil, errors.New("server does not support object-info")
	}
	if c.broken.Load() {
		return nil, errors.New("session closed")
	}
	defer c.watch(ctx)()
	args := []string{"size"}
	for _, o := range oids {
		args = append(args, "oid "+o.String())
	}
	if err := c.begin("object-info", fmt.Sprintf("object-info oids=%d", len(oids)), args); err != nil {
		return nil, c.wrapErr(err)
	}
	out := map[object.OID]int64{}
	total := 0
	for {
		k, p, err := c.pr.ReadPkt()
		if err != nil {
			return nil, c.wrapErr(err)
		}
		if k == pktline.Flush {
			return out, nil
		}
		if k != pktline.Data {
			continue
		}
		total += len(p) + 4
		if total > maxListBytes {
			return nil, fmt.Errorf("server sent an oversized object-info response (over %d bytes)", maxListBytes)
		}
		f := strings.Fields(string(p))
		if len(f) == 2 {
			if oid, err := object.ParseOID(f[0]); err == nil {
				if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					out[oid] = n
				}
			}
		}
	}
}
