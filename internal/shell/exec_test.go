package shell

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/autowert66/explorerepo/internal/builtins"
)

// A panic in one builtin must not take down the process: it is reported on
// stderr and turned into an ordinary failure status.
func TestRunBuiltinRecoversPanic(t *testing.T) {
	var stderr bytes.Buffer
	env := &builtins.Env{
		Stderr: &stderr,
		Stdout: builtins.NewOut(&bytes.Buffer{}),
	}
	boom := func(context.Context, *builtins.Env, []string) int { panic("boom") }

	if got := runBuiltin(context.Background(), env, boom, []string{"boom"}); got != builtins.ExitFail {
		t.Fatalf("status = %d, want %d", got, builtins.ExitFail)
	}
	if !strings.Contains(stderr.String(), "internal error") || !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
