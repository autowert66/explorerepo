package pack

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/testutil"
)

func TestDeltaCopySizeZero(t *testing.T) {
	base := bytes.Repeat([]byte("x"), 0x10000)
	// header: base size 0x10000 (varint 80 80 04), result size 0x10000; copy op with no size bytes => 0x10000
	delta := []byte{0x80, 0x80, 0x04, 0x80, 0x80, 0x04, 0x80}
	out, err := ApplyDelta(base, delta)
	if err != nil || !bytes.Equal(out, base) {
		t.Fatalf("err=%v len=%d", err, len(out))
	}
}

// A delta may not claim (or grow to) more than the caller's limit, even if
// its instruction stream is small.
func TestDeltaRejectsOversizedResult(t *testing.T) {
	base := bytes.Repeat([]byte("x"), 4096)
	// base size 4096, result size 4096, then one copy of all 4096 bytes.
	delta := []byte{0x80, 0x20, 0x80, 0x20, 0xb0, 0x00, 0x10}
	if out, err := ApplyDelta(base, delta); err != nil || len(out) != 4096 {
		t.Fatalf("unlimited apply: err=%v len=%d", err, len(out))
	}
	if _, err := ApplyDeltaLimited(base, delta, 1024); err == nil {
		t.Fatal("expected an error for a result larger than the limit")
	}
}

// A delta that copies repeatedly must stop as soon as it passes the declared
// result size instead of growing first and checking at the end.
func TestDeltaStopsAtDeclaredSize(t *testing.T) {
	base := bytes.Repeat([]byte("x"), 0x10000)
	// base size 0x10000, result size 1, then many 64 KiB copy ops.
	delta := []byte{0x80, 0x80, 0x04, 0x01}
	for i := 0; i < 1000; i++ {
		delta = append(delta, 0x80)
	}
	if _, err := ApplyDelta(base, delta); err == nil {
		t.Fatal("expected an error once the result exceeds the declared size")
	}
}

func TestDeltaErrors(t *testing.T) {
	if _, err := ApplyDelta([]byte("abc"), []byte{3, 3, 0}); err == nil {
		t.Error("opcode 0 should fail")
	}
	if _, err := ApplyDelta([]byte("abc"), []byte{4, 3}); err == nil {
		t.Error("base size mismatch should fail")
	}
	if _, err := ApplyDelta([]byte("abc"), []byte{3, 3, 0x91, 2, 5}); err == nil {
		t.Error("copy out of range should fail")
	}
}

func packObjects(t *testing.T, f *testutil.Fixture, extra ...string) []byte {
	t.Helper()
	args := append([]string{"pack-objects", "--stdout", "--revs", "--window=50"}, extra...)
	cmd := exec.Command("git", args...)
	cmd.Dir = f.Bare
	cmd.Stdin = strings.NewReader("main\nfeature\nv1\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func checkPack(t *testing.T, f *testutil.Fixture, data []byte) {
	t.Helper()
	got := map[object.OID]Raw{}
	err := Parse(bytes.NewReader(data), Options{}, func(r Raw) error { got[r.OID] = r; return nil })
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Fields(testutil.Git(t, f.Bare, nil, "rev-list", "--objects", "--all"))
	n := 0
	for _, s := range all {
		oid, err := object.ParseOID(s)
		if err != nil {
			continue // path names
		}
		n++
		r, ok := got[oid]
		if !ok {
			t.Fatalf("object %s missing", oid)
		}
		want := testutil.Git(t, f.Bare, nil, "cat-file", "-s", oid.String())
		if want != itoa(len(r.Data)) {
			t.Fatalf("%s size %d want %s", oid, len(r.Data), want)
		}
	}
	if len(got) < n {
		t.Fatalf("got %d objects, want >= %d", len(got), n)
	}
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + string(appendInt(nil, n))) }
func appendInt(b []byte, n int) []byte {
	if n >= 10 {
		b = appendInt(b, n/10)
	}
	return append(b, byte('0'+n%10))
}

func TestParseOfsDelta(t *testing.T) {
	f := testutil.NewFixture(t)
	data := packObjects(t, f, "--delta-base-offset")
	checkPack(t, f, data)
}

func TestParseRefDelta(t *testing.T) {
	f := testutil.NewFixture(t)
	data := packObjects(t, f) // no --delta-base-offset => REF_DELTA
	checkPack(t, f, data)
}

// MaxTotalBytes bounds the memory a pack can make the parser retain.
func TestParseTotalBudget(t *testing.T) {
	f := testutil.NewFixture(t)
	data := packObjects(t, f, "--delta-base-offset")
	if err := Parse(bytes.NewReader(data), Options{MaxTotalBytes: 1},
		func(Raw) error { return nil }); err == nil {
		t.Fatal("expected the total-data budget to reject the pack")
	}
}

func TestChecksumMismatch(t *testing.T) {
	f := testutil.NewFixture(t)
	data := packObjects(t, f, "--delta-base-offset")
	data[len(data)-1] ^= 0xff
	if err := Parse(bytes.NewReader(data), Options{}, func(Raw) error { return nil }); err == nil {
		t.Fatal("expected checksum error")
	}
}

func TestTruncated(t *testing.T) {
	f := testutil.NewFixture(t)
	data := packObjects(t, f)
	if err := Parse(bytes.NewReader(data[:len(data)/2]), Options{}, func(Raw) error { return nil }); err == nil {
		t.Fatal("expected error")
	}
}
