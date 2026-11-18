package hmicheck

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A browser that never says it is listening: launch gives up after
// NAUT_BROWSER_START_TIMEOUT, and its error names the variable, so a
// person on a slow machine knows what to set.
func TestLaunchWaitsAsLongAsTheOverrideSays(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in browser is a shell script")
	}
	slow := filepath.Join(t.TempDir(), "slow-browser")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(StartTimeoutEnv, "1s")
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		b, err := launch(context.Background(), slow)
		if b != nil {
			b.close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("launch of a browser that never starts: no error")
		}
		if !strings.Contains(err.Error(), "didn't start in 1s") || !strings.Contains(err.Error(), StartTimeoutEnv) {
			t.Fatalf("error = %q, want it to say 1s and name %s", err, StartTimeoutEnv)
		}
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("gave up after %s, want about 1s", took)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("launch still waiting after 15 s with %s=1s: the override is ignored", StartTimeoutEnv)
	}
}

func TestStartTimeout(t *testing.T) {
	t.Setenv(StartTimeoutEnv, "")
	if d, err := startTimeout(); err != nil || d != DefaultStartTimeout || d < 2*time.Minute {
		t.Fatalf("unset: %s, %v; want %s (at least 2m: a loaded CI runner took 90 s)", d, err, DefaultStartTimeout)
	}
	t.Setenv(StartTimeoutEnv, "5m")
	if d, err := startTimeout(); err != nil || d != 5*time.Minute {
		t.Fatalf("5m: %s, %v", d, err)
	}
	for _, bad := range []string{"soon", "0s", "-1m", "120"} {
		t.Setenv(StartTimeoutEnv, bad)
		if _, err := startTimeout(); err == nil || !strings.Contains(err.Error(), StartTimeoutEnv) {
			t.Fatalf("%q: err = %v, want one naming %s", bad, err, StartTimeoutEnv)
		}
	}
}
