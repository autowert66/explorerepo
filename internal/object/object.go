// Package object holds git object identifiers and parsers for trees, commits and tags.
package object

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// OID is a SHA-1 object id.
type OID [20]byte

func (o OID) String() string { return hex.EncodeToString(o[:]) }
func (o OID) IsZero() bool   { return o == OID{} }

// ParseOID parses a full 40-hex object id.
func ParseOID(s string) (OID, error) {
	var o OID
	if len(s) != 40 {
		return o, fmt.Errorf("invalid object id %q", s)
	}
	if _, err := hex.Decode(o[:], []byte(s)); err != nil {
		return o, fmt.Errorf("invalid object id %q", s)
	}
	return o, nil
}

// IsHex reports whether s is non-empty and entirely hex digits.
func IsHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

type Type int

const (
	Commit Type = 1
	Tree   Type = 2
	Blob   Type = 3
	Tag    Type = 4
)

func (t Type) String() string {
	switch t {
	case Commit:
		return "commit"
	case Tree:
		return "tree"
	case Blob:
		return "blob"
	case Tag:
		return "tag"
	}
	return "unknown"
}

func ParseType(s string) (Type, error) {
	switch s {
	case "commit":
		return Commit, nil
	case "tree":
		return Tree, nil
	case "blob":
		return Blob, nil
	case "tag":
		return Tag, nil
	}
	return 0, fmt.Errorf("unknown object type %q", s)
}

// Header returns the "<type> <len>\0" prefix used for hashing and loose storage.
func Header(t Type, n int) []byte { return []byte(t.String() + " " + strconv.Itoa(n) + "\x00") }

// Hash computes the object id of (type, data).
func Hash(t Type, data []byte) OID {
	h := sha1.New()
	h.Write(Header(t, len(data)))
	h.Write(data)
	var o OID
	h.Sum(o[:0])
	return o
}

// Tree entry modes.
const (
	ModeDir     = 0o40000
	ModeFile    = 0o100644
	ModeExec    = 0o100755
	ModeSymlink = 0o120000
	ModeGitlink = 0o160000
)

type Entry struct {
	Name string
	Mode uint32
	OID  OID
}

func (e Entry) IsDir() bool     { return e.Mode == ModeDir }
func (e Entry) IsSymlink() bool { return e.Mode&0o170000 == ModeSymlink }
func (e Entry) IsGitlink() bool { return e.Mode == ModeGitlink }
func (e Entry) IsRegular() bool { return e.Mode&0o170000 == 0o100000 }
func (e Entry) IsExec() bool    { return e.IsRegular() && e.Mode&0o111 != 0 }

// ParseTree decodes a tree object body.
func ParseTree(data []byte) ([]Entry, error) {
	var out []Entry
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		if sp <= 0 {
			return nil, errors.New("malformed tree: missing mode")
		}
		mode, err := strconv.ParseUint(string(data[:sp]), 8, 32)
		if err != nil {
			return nil, errors.New("malformed tree: bad mode")
		}
		data = data[sp+1:]
		nul := bytes.IndexByte(data, 0)
		if nul < 0 || len(data) < nul+1+20 {
			return nil, errors.New("malformed tree: truncated entry")
		}
		e := Entry{Name: string(data[:nul]), Mode: uint32(mode)}
		copy(e.OID[:], data[nul+1:nul+21])
		out = append(out, e)
		data = data[nul+21:]
	}
	return out, nil
}

type CommitInfo struct {
	Tree       OID
	Parents    []OID
	Author     string
	Committer  string
	Time       time.Time // committer time
	AuthorTime time.Time
	Message    string
}

// ParseCommit decodes a commit object body.
func ParseCommit(data []byte) (*CommitInfo, error) {
	c := &CommitInfo{}
	head, msg, _ := strings.Cut(string(data), "\n\n")
	c.Message = msg
	haveTree := false
	for _, line := range strings.Split(head, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "tree":
			o, err := ParseOID(val)
			if err != nil {
				return nil, errors.New("malformed commit: bad tree")
			}
			c.Tree, haveTree = o, true
		case "parent":
			o, err := ParseOID(val)
			if err != nil {
				return nil, errors.New("malformed commit: bad parent")
			}
			c.Parents = append(c.Parents, o)
		case "author":
			c.Author = val
			c.AuthorTime = parseSigTime(val)
		case "committer":
			c.Committer = val
			c.Time = parseSigTime(val)
		}
	}
	if !haveTree {
		return nil, errors.New("malformed commit: no tree")
	}
	return c, nil
}

// parseSigTime parses "Name <email> 1234567890 +0100".
func parseSigTime(sig string) time.Time {
	gt := strings.LastIndexByte(sig, '>')
	if gt < 0 {
		return time.Time{}
	}
	f := strings.Fields(sig[gt+1:])
	if len(f) < 1 {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return time.Time{}
	}
	t := time.Unix(secs, 0).UTC()
	if len(f) > 1 && len(f[1]) == 5 {
		h, _ := strconv.Atoi(f[1][1:3])
		m, _ := strconv.Atoi(f[1][3:5])
		off := h*3600 + m*60
		if f[1][0] == '-' {
			off = -off
		}
		t = t.In(time.FixedZone("", off))
	}
	return t
}

// ParseTag decodes an annotated tag body into its target.
func ParseTag(data []byte) (OID, Type, error) {
	var oid OID
	var typ Type
	head, _, _ := strings.Cut(string(data), "\n\n")
	have := 0
	for _, line := range strings.Split(head, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "object":
			o, err := ParseOID(val)
			if err != nil {
				return oid, 0, errors.New("malformed tag: bad object")
			}
			oid = o
			have++
		case "type":
			t, err := ParseType(val)
			if err != nil {
				return oid, 0, err
			}
			typ = t
			have++
		}
	}
	if have != 2 {
		return oid, 0, errors.New("malformed tag")
	}
	return oid, typ, nil
}
