package sparkplug_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug"
)

// tckVersion is the sparkplug-tck-go release the conformance test runs
// against. Bump alongside any protocol changes.
const tckVersion = "v0.1.2"

// TestTCKConformance drives an in-process edge node through its full lifecycle
// against the real Sparkplug TCK edge-node profile and asserts zero failures.
//
// It is gated on NAUTILUS_TCK=1 because it fetches and runs the external TCK
// harness (needs the module proxy) — normal `go test ./...` skips it. CI sets
// the flag. The TCK embeds its own MQTT broker, so no external broker is
// needed.
func TestTCKConformance(t *testing.T) {
	if os.Getenv("NAUTILUS_TCK") != "1" {
		t.Skip("set NAUTILUS_TCK=1 to run the Sparkplug TCK conformance test")
	}
	const (
		group  = "TestGroup"
		node   = "TestNode"
		listen = "127.0.0.1:18899"
		broker = "tcp://" + listen
	)
	out := filepath.Join(t.TempDir(), "tck.json")

	// Start the TCK harness (embedded broker + edge-node profile), scheduling
	// a rebirth stimulus partway through.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	harness := exec.CommandContext(ctx, "go", "run",
		"github.com/joyautomation/sparkplug-tck-go/cmd/sparkplug-tck@"+tckVersion,
		"-harness", "-profile", "edge-node", "-listen", listen,
		"-duration", "24s", "-rebirth", group+"/"+node, "-rebirth-after", "8s", "-json")
	harness.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	outFile, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	harness.Stdout = outFile
	harness.Stderr = testWriter{t}
	if err := harness.Start(); err != nil {
		t.Fatalf("start TCK harness: %v", err)
	}
	defer func() { _ = harness.Wait() }()

	// Give the harness a moment to bind its broker.
	if !waitForListen(listen, 15*time.Second) {
		t.Fatal("TCK harness broker never came up")
	}

	// Run our node against the harness broker.
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM T\nVAR_EXTERNAL\n  Speed : REAL;\n  Enable : BOOL;\nEND_VAR\nEND_PROGRAM",
		Driver:  nio.NewMemory(),
		Scan:    100 * time.Millisecond,
		Seed:    nio.Values{"Speed": 10.0, "Enable": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	rtCtx, rtCancel := context.WithCancel(context.Background())
	go rt.Run(rtCtx)

	n, err := sparkplug.New(rt, sparkplug.Config{
		BrokerURL: broker, GroupID: group, EdgeNode: node,
		PublishInterval: 200 * time.Millisecond,
	}, sparkplug.WithDefaultRBE(sparkplug.RBE{MaxInterval: 2 * time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(rtCtx); err != nil {
		t.Fatalf("node start: %v", err)
	}

	// Change a value so NDATA flows; let the rebirth fire; then shut down
	// gracefully (NDEATH + clean disconnect) while the harness still watches.
	go func() {
		tk := time.NewTicker(300 * time.Millisecond)
		defer tk.Stop()
		for i := 0; ; i++ {
			select {
			case <-rtCtx.Done():
				return
			case <-tk.C:
				rt.Tags().SetReal("Speed", 10+float64(i))
			}
		}
	}()
	time.Sleep(16 * time.Second) // through birth + rebirth
	n.Stop()
	rtCancel()

	if err := harness.Wait(); err != nil {
		// A non-zero exit means the profile found failures; we still parse to
		// report which.
		t.Logf("TCK harness exit: %v", err)
	}
	assertNoFailures(t, out)
}

func assertNoFailures(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read TCK results: %v", err)
	}
	var items []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatalf("parse TCK results: %v\n%s", err, string(b))
	}
	pass, na, fail := 0, 0, 0
	for _, it := range items {
		switch strings.ToLower(it.Status) {
		case "pass":
			pass++
		case "fail":
			fail++
			t.Errorf("TCK FAIL %s: %s", it.ID, it.Detail)
		default:
			na++
		}
	}
	t.Logf("TCK edge-node: %d pass, %d n/a, %d fail", pass, na, fail)
	if fail > 0 {
		t.Fatalf("%d TCK assertion(s) failed", fail)
	}
	if pass == 0 {
		t.Fatal("no assertions passed — did the node connect?")
	}
}

func waitForListen(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// testWriter pipes subprocess stderr into the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.t.Log("tck: " + line)
		}
	}
	return len(p), nil
}
