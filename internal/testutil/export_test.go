package testutil

import (
	"os"
	"os/exec"
	"testing"
)

// EXPLOREREPO_FIXTURE_OUT=/some/dir go test ./internal/testutil -run Export
// writes the fixture's bare repo to /some/dir/bare.git for manual experiments.
func TestExportFixture(t *testing.T) {
	out := os.Getenv("EXPLOREREPO_FIXTURE_OUT")
	if out == "" {
		t.Skip("set EXPLOREREPO_FIXTURE_OUT")
	}
	f := NewFixture(t)
	os.RemoveAll(out)
	os.MkdirAll(out, 0o755)
	if b, err := exec.Command("cp", "-R", f.Bare, out+"/bare.git").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
}
