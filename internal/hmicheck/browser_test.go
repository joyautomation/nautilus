package hmicheck

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

// fakeBrowser is a stand-in browser that says DevTools is listening at a
// test server, which takes `delay` to answer the websocket handshake (or
// never answers, delay < 0), then answers every command with an empty
// result.
func fakeBrowser(t *testing.T, delay time.Duration) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in browser is a shell script")
	}
	up := websocket.Upgrader{}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay < 0 {
			<-release
			return
		}
		time.Sleep(delay)
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			var m message
			if c.ReadJSON(&m) != nil {
				return
			}
			_ = c.WriteJSON(message{ID: m.ID, Result: []byte("{}")})
		}
	}))
	t.Cleanup(func() { close(release); srv.CloseClientConnections(); srv.Close() })
	path := filepath.Join(t.TempDir(), "devtools-browser")
	script := fmt.Sprintf("#!/bin/sh\necho 'DevTools listening on ws://%s/devtools/browser/x' >&2\nexec sleep 60\n", srv.Listener.Addr().(*net.TCPAddr).String())
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A loaded CI runner under -race took 47 s to print its DevTools line and
// then more than 10 s to answer the handshake (#964). The handshake gets
// what is left of the start budget, not a fixed 10 s.
func TestLaunchWaitsForASlowHandshake(t *testing.T) {
	t.Setenv(StartTimeoutEnv, "")
	path := fakeBrowser(t, 11*time.Second)
	b, err := launch(context.Background(), path)
	if err != nil {
		t.Fatalf("a handshake answered in 11 s, within the %s start budget: %v", DefaultStartTimeout, err)
	}
	b.close()
}

// A handshake that never comes: the error says the browser didn't answer
// (after the 10 s floor, the 1 s budget being spent), and names the
// override, like a browser that never starts.
func TestLaunchNamesTheOverrideWhenTheHandshakeRunsOut(t *testing.T) {
	t.Setenv(StartTimeoutEnv, "1s")
	path := fakeBrowser(t, -1)
	start := time.Now()
	_, err := launch(context.Background(), path)
	if err == nil {
		t.Fatal("a handshake that never comes: no error")
	}
	if !strings.Contains(err.Error(), "didn't answer on DevTools in 10s") || !strings.Contains(err.Error(), StartTimeoutEnv) {
		t.Fatalf("error = %q, want it to say the browser didn't answer and name %s", err, StartTimeoutEnv)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Fatalf("gave up after %s", took)
	}
}
