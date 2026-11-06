// Package pktline implements git's pkt-line framing.
package pktline

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

type Kind int

const (
	Data Kind = iota
	Flush
	Delim
	ResponseEnd
)

const MaxPkt = 65520

type Reader struct{ r *bufio.Reader }

func NewReader(r io.Reader) *Reader { return &Reader{bufio.NewReaderSize(r, 64*1024)} }

// ReadPkt returns the next packet. For Data the payload is returned as-is
// (including any trailing newline).
func (r *Reader) ReadPkt() (Kind, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
		if err == io.EOF {
			return 0, nil, io.EOF
		}
		return 0, nil, fmt.Errorf("reading pkt-line header: %w", unexpected(err))
	}
	n, ok := parseLen(hdr)
	if !ok {
		return 0, nil, fmt.Errorf("malformed pkt-line length %q", hdr[:])
	}
	switch n {
	case 0:
		return Flush, nil, nil
	case 1:
		return Delim, nil, nil
	case 2:
		return ResponseEnd, nil, nil
	case 3:
		return 0, nil, errors.New("malformed pkt-line: length 3")
	}
	if n > MaxPkt+4 {
		return 0, nil, fmt.Errorf("pkt-line too long (%d)", n)
	}
	buf := make([]byte, n-4)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		return 0, nil, fmt.Errorf("reading pkt-line payload: %w", unexpected(err))
	}
	return Data, buf, nil
}

func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

func parseLen(h [4]byte) (int, bool) {
	n := 0
	for _, c := range h {
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'f':
			v = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = int(c-'A') + 10
		default:
			return 0, false
		}
		n = n<<4 | v
	}
	return n, true
}

type Writer struct{ w io.Writer }

func NewWriter(w io.Writer) *Writer { return &Writer{w} }

func (w *Writer) Data(b []byte) error {
	if len(b) > MaxPkt-4 {
		return errors.New("pkt-line payload too large")
	}
	_, err := fmt.Fprintf(w.w, "%04x%s", len(b)+4, b)
	return err
}

func (w *Writer) Line(s string) error { return w.Data([]byte(s + "\n")) }
func (w *Writer) Flush() error        { _, err := io.WriteString(w.w, "0000"); return err }
func (w *Writer) Delim() error        { _, err := io.WriteString(w.w, "0001"); return err }
