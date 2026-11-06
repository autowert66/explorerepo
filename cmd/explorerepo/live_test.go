package main

import (
	"os"
	"strings"
	"testing"
)

// Live smoke tests against github.com over ssh. Run with EXPLOREREPO_LIVE=1.
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("EXPLOREREPO_LIVE") == "" {
		t.Skip("set EXPLOREREPO_LIVE=1 to run against github.com")
	}
	out, errOut, code := exec(t, "", "--repo", "octocat/Hello-World", "cat", "README")
	if code != 0 || !strings.Contains(out, "Hello World!") {
		t.Fatalf("Hello-World README: %d %q %q", code, out, errOut)
	}
	out, errOut, code = exec(t, "", "--repo", "golang/go", "ls", "src")
	if code != 0 || !strings.Contains(out, "fmt\n") || !strings.Contains(out, "net\n") {
		t.Fatalf("golang/go ls src: %d %q %q", code, out[:min(200, len(out))], errOut)
	}
	out, errOut, code = exec(t, "", "--repo", "golang/go", "-c", "grep -rn 'func Println' src/fmt")
	if code != 0 || !strings.Contains(out, "src/fmt/print.go:") || !strings.Contains(out, "func Println(") {
		t.Fatalf("golang/go grep: %d %q %q", code, out, errOut)
	}
	out, errOut, code = exec(t, "", "--repo", "golang/go", "--rev", "go1.21.0", "-c", "cat VERSION | head -1")
	if code != 0 || !strings.HasPrefix(out, "go1.21.0") {
		t.Fatalf("golang/go --rev go1.21.0: %d %q %q", code, out, errOut)
	}
}
