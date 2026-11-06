package object

import (
	"fmt"
	"testing"
)

func TestHashKnownVectors(t *testing.T) {
	// git hash-object of "" and "hello\n"
	if got := Hash(Blob, nil).String(); got != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Errorf("empty blob = %s", got)
	}
	if got := Hash(Blob, []byte("hello\n")).String(); got != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Errorf("hello blob = %s", got)
	}
	if got := Hash(Tree, nil).String(); got != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" {
		t.Errorf("empty tree = %s", got)
	}
}

func TestParseOID(t *testing.T) {
	for _, bad := range []string{"", "abc", "zz" + string(make([]byte, 38)), "e69de29bb2d1d6434b8b29ae775ad8c2e48c539"} {
		if _, err := ParseOID(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	o, err := ParseOID("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391")
	if err != nil || o.String() != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Errorf("%v %v", o, err)
	}
}

func TestParseTree(t *testing.T) {
	var b []byte
	oid := Hash(Blob, []byte("x"))
	for _, e := range []struct {
		mode, name string
	}{{"100644", "a.txt"}, {"40000", "dir"}, {"120000", "link"}, {"160000", "sub"}, {"100755", "run.sh"}} {
		b = append(b, []byte(fmt.Sprintf("%s %s\x00", e.mode, e.name))...)
		b = append(b, oid[:]...)
	}
	es, err := ParseTree(b)
	if err != nil || len(es) != 5 {
		t.Fatalf("%v %d", err, len(es))
	}
	if !es[1].IsDir() || !es[2].IsSymlink() || !es[3].IsGitlink() || !es[4].IsExec() || es[0].IsExec() || !es[0].IsRegular() {
		t.Errorf("mode predicates wrong: %+v", es)
	}
	for _, bad := range [][]byte{[]byte("100644 a"), []byte("100644 a\x00short"), []byte("xx a\x00" + string(oid[:])), []byte("nospace")} {
		if _, err := ParseTree(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestParseCommit(t *testing.T) {
	data := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"parent e69de29bb2d1d6434b8b29ae775ad8c2e48c5391\n" +
		"parent ce013625030ba8dba906f756967f9e9ca394464a\n" +
		"author A U <a@x> 1700000000 +0100\n" +
		"committer C <c@x> 1700003600 -0530\n\nsubject\n\nbody\n"
	c, err := ParseCommit([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Parents) != 2 || c.Message != "subject\n\nbody\n" {
		t.Errorf("%+v", c)
	}
	if c.Time.Unix() != 1700003600 || c.AuthorTime.Unix() != 1700000000 {
		t.Errorf("times %v %v", c.Time, c.AuthorTime)
	}
	if _, off := c.Time.Zone(); off != -(5*3600 + 30*60) {
		t.Errorf("zone offset %d", off)
	}
	if _, err := ParseCommit([]byte("author x\n\nmsg")); err == nil {
		t.Error("commit without tree should fail")
	}
}

func TestParseTag(t *testing.T) {
	o, ty, err := ParseTag([]byte("object e69de29bb2d1d6434b8b29ae775ad8c2e48c5391\ntype commit\ntag v1\ntagger x <y> 1 +0000\n\nmsg\n"))
	if err != nil || ty != Commit || o.String() != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Errorf("%v %v %v", o, ty, err)
	}
	if _, _, err := ParseTag([]byte("tag v1\n\nmsg")); err == nil {
		t.Error("incomplete tag should fail")
	}
}
