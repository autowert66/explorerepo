package pktline

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Line("hello")
	w.Delim()
	w.Data([]byte("raw"))
	w.Flush()
	r := NewReader(&buf)
	k, p, err := r.ReadPkt()
	if err != nil || k != Data || string(p) != "hello\n" {
		t.Fatalf("got %v %q %v", k, p, err)
	}
	if k, _, _ := r.ReadPkt(); k != Delim {
		t.Fatalf("want delim, got %v", k)
	}
	if k, p, _ := r.ReadPkt(); k != Data || string(p) != "raw" {
		t.Fatalf("got %v %q", k, p)
	}
	if k, _, _ := r.ReadPkt(); k != Flush {
		t.Fatalf("want flush")
	}
	if _, _, err := r.ReadPkt(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestMalformed(t *testing.T) {
	for _, in := range []string{"zzzz", "0003", "00", "0010abc", "fff0"} {
		_, _, err := NewReader(strings.NewReader(in)).ReadPkt()
		if err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestEmptyPayloadLen4(t *testing.T) {
	k, p, err := NewReader(strings.NewReader("0004")).ReadPkt()
	if err != nil || k != Data || len(p) != 0 {
		t.Fatalf("got %v %q %v", k, p, err)
	}
}
