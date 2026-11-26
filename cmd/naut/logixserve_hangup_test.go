package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/logix/facade"
	"github.com/joyautomation/nautilus/logix/logixd"
)

// #1404, end to end: `naut logix serve --project`, the editor's Download
// Project, and a browser that closes its tab while the controller is being
// downloaded to. The download runs to its end, verifies, and the journal
// records it as it really went: ok.
func TestLogixServeDownloadOutlivesAHangUp(t *testing.T) {
	host, port, _ := emulateSpec(t, moduleIOController())
	f, agent, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	t.Setenv("NAUTILUS_TOKEN", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(agent, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644); err != nil {
		t.Fatal(err)
	}
	base := serveLogix(t, "--host", host, "--port", port, "--poll", "30ms", "--project", dir)

	ctx, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	downloads := 0
	f.hook = func(r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/download") {
			return
		}
		downloads++
		// The controller now holds what was sent, as a real one does
		// after a download, so the verify after it passes.
		for name, raw := range f.files {
			if strings.HasSuffix(name, "/Line.L5X") {
				f.controllerL5X = string(raw)
			}
		}
		hangUp() // the browser goes away mid-download
		select {
		case <-r.Context().Done(): // the serve dropped the call: the bug
		case <-time.After(500 * time.Millisecond):
		}
	}
	raw, _ := json.Marshal(map[string]any{"mode": "download", "source": fillLadder, "yes": true, "programMode": true})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/logix/deploy", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if res, err := http.DefaultClient.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, res.Body) // ends with the hang-up
		res.Body.Close()
	}

	var entries []map[string]any
	for i := 0; i < 200 && len(entries) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		res, err := http.Get(base + "/api/audit?action=logix.deploy")
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Entries []map[string]any `json:"entries"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		entries = out.Entries
	}
	if downloads != 1 {
		t.Fatalf("%d download call(s) reached the agent; want 1", downloads)
	}
	if len(entries) != 1 || entries[0]["result"] != "ok" {
		t.Fatalf("journal after the hang-up: %v; want one entry, result ok", entries)
	}
	if !seenAfter(f.seen, "/download", "/upload-to-new") {
		t.Errorf("no verify upload after the download: %v", f.seen)
	}
}

// seenAfter says a call ending in later came after one ending in first.
func seenAfter(seen []string, first, later string) bool {
	at := -1
	for i, s := range seen {
		if at < 0 && strings.HasSuffix(s, first) {
			at = i
		}
		if at >= 0 && i > at && strings.HasSuffix(s, later) {
			return true
		}
	}
	return false
}

// A download whose verify finds the controller different is in doubt on
// the command line too: OUTCOME UNKNOWN with what to do, and the
// differences, not the old "VERIFY FAILED" that reads as a clean failure.
func TestLogixDeployVerifyMismatchIsOutcomeUnknown(t *testing.T) {
	f, agent, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine // the controller never takes what is sent
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(agent, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644); err != nil {
		t.Fatal(err)
	}
	var out string
	var code int
	stdout := captureStdout(t, func() int {
		out, code = captureStderr(t, func() int {
			return runLogixDeploy([]string{"--download", "--yes", "--program-mode", dir})
		})
		return code
	})
	if code != 1 || !strings.Contains(out, "OUTCOME UNKNOWN") || !strings.Contains(out, "may hold the new program") ||
		!strings.Contains(out, "routine Fill/MainRoutine") || strings.Contains(stdout, "VERIFY FAILED") {
		t.Fatalf("exit %d\nstderr: %s\nstdout: %s", code, out, stdout)
	}
}

// Stopping the serve while a download writes to the controller waits for
// it, and says why and how not to.
func TestLogixServeStopWaitsForADeployUnderWay(t *testing.T) {
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	f, agent, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(agent, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := loadLogixProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	entered, hold := make(chan struct{}), make(chan struct{})
	f.hook = func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			close(entered)
			<-hold
		}
	}
	stderr, _ := captureStderr(t, func() int {
		plane, release := logixProgramPlane(dir, p, logixd.New(agent, ""))
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = plane.Download(context.Background(), fillLadder, facade.DownloadOptions{ProgramMode: true}, func(string) {})
		}()
		<-entered
		released := make(chan struct{})
		go func() { release(); close(released) }()
		select {
		case <-released:
			t.Error("the stop did not wait for the download")
		case <-time.After(200 * time.Millisecond):
		}
		close(hold)
		<-done
		<-released
		return 0
	})
	if !strings.Contains(stderr, "waiting for the deploy under way to end") {
		t.Errorf("stderr = %q", stderr)
	}
}
