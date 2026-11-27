package main

// The program plane `naut logix serve --project` builds (logixProgramPlane):
// what the editor's Download Project and Push Online Edit commands reach
// through POST /api/logix/deploy, driven here against the fake agent. The
// facade tests prove the endpoint; these prove the session handling behind
// it — a download lets go of the held online session first (it is online
// with the project the download replaces) and opens a fresh one after, so
// the next online edit is warm — and that every deploy line reaches the
// editor's progress.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/logix/deploy"
	"github.com/joyautomation/nautilus/logix/facade"
	"github.com/joyautomation/nautilus/logix/logixd"
)

// emptyLine is a controller named Line with no program in it yet.
const emptyLine = `<?xml version="1.0"?><RSLogix5000Content><Controller Name="Line"><Programs/></Controller></RSLogix5000Content>`

func TestLogixServePlaneDownloadRewarmsTheSession(t *testing.T) {
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	f, url, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(url, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := loadLogixProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	progress := func(s string) { lines = append(lines, s) }
	stderr, _ := captureStderr(t, func() int {
		plane, release := logixProgramPlane(dir, p, logixd.New(url, ""))
		defer release()
		if plane.Controller != "Line" || !strings.Contains(plane.CommPath, "10.0.0.5") || plane.Download == nil {
			t.Fatalf("plane = %+v", plane)
		}
		ctx := context.Background()

		// The first online edit opens the session. The fake controller has
		// no Fill routine, so the edit itself is refused: a download is due.
		if _, err := plane.Deploy(ctx, fillLadder, progress); err == nil {
			t.Fatal("an edit to a controller without the routine was accepted")
		} else if _, ok := err.(*deploy.NeedsDownloadError); !ok {
			t.Fatalf("first edit: %v", err)
		}
		if !hasLine(lines, "opening the online session") {
			t.Errorf("progress = %q", lines)
		}

		// The download's verification compares the controller's upload with
		// what was sent: let the fake controller hold what the deploy
		// generates, as a real one does after the download.
		_, _ = plane.Download(ctx, fillLadder, facade.DownloadOptions{ProgramMode: true}, func(string) {})
		gen := ""
		for name, raw := range f.files {
			if strings.HasSuffix(name, "/Line.L5X") {
				gen = string(raw)
			}
		}
		if gen == "" {
			t.Fatalf("no project reached the agent: %v", f.seen)
		}
		f.controllerL5X = gen

		f.seen, lines = nil, nil
		rep, err := plane.Download(ctx, fillLadder, facade.DownloadOptions{ProgramMode: true}, progress)
		if err != nil || rep.Applied != deploy.Download || !rep.Verified {
			t.Fatalf("download: %v %+v", err, rep)
		}
		// The download reports as soon as it has verified: the session
		// for the next edit is Warm's, once the report is out (#379).
		if indexOf(f.seen, "/download") < 0 || indexOf(f.seen, "/files/session-") >= 0 {
			t.Errorf("the download reopened the session before reporting: %v", f.seen)
		}
		if hasLine(lines, "opening the online session for the next edit") {
			t.Errorf("download progress = %q", lines)
		}
		lines = nil
		plane.Warm(ctx, progress)
		if indexOf(f.seen, "/files/session-") < 0 || indexOf(f.seen, "/online") < 0 {
			t.Errorf("no session reopened after the download: %v", f.seen)
		}
		if !hasLine(lines, "opening the online session for the next edit") || !hasLine(lines, "session open and online") {
			t.Errorf("warm progress = %q", lines)
		}

		// The next edit is warm: no upload, just the rung import.
		f.seen = nil
		edited := strings.Replace(fillLadder, "RUNG step Start", "RUNG step /Start", 1)
		lines = nil
		_, _ = plane.Deploy(ctx, edited, progress)
		if indexOf(f.seen, "/upload-to-new") >= 0 || indexOf(f.seen, "/import-rungs") < 0 {
			t.Errorf("the edit after a download was not warm: %v", f.seen)
		}
		// A warm edit says what it does as it does it, not only at the end.
		if !hasLine(lines, "replacing Fill/") || !hasLine(lines, "verifying from the controller") {
			t.Errorf("warm edit progress = %q", lines)
		}
		return 0
	})
	if !strings.Contains(stderr, "deploy: ") {
		t.Errorf("deploy lines did not reach stderr: %q", stderr)
	}
}

// An edit that arrives while a download's warm-up opens the session waits
// for it, says so on its own stream, and then edits warm; each stream gets
// its own lines.
func TestLogixServePlaneEditWaitsForTheWarmUp(t *testing.T) {
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	f, url, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(url, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644)
	p, err := loadLogixProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() int {
		plane, release := logixProgramPlane(dir, p, logixd.New(url, ""))
		defer release()
		ctx := context.Background()
		// A controller that runs what the project generates, as after a
		// download.
		_, _ = plane.Download(ctx, fillLadder, facade.DownloadOptions{ProgramMode: true}, func(string) {})
		for name, raw := range f.files {
			if strings.HasSuffix(name, "/Line.L5X") {
				f.controllerL5X = string(raw)
			}
		}

		f.uploadEntered, f.uploadHold = make(chan struct{}, 4), make(chan struct{})
		var warmLines []string
		warmed := make(chan struct{})
		go func() {
			plane.Warm(ctx, func(s string) { warmLines = append(warmLines, s) })
			close(warmed)
		}()
		select {
		case <-f.uploadEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("the warm-up never uploaded")
		}
		editLines := make(chan string, 64)
		edited := make(chan error, 1)
		go func() {
			_, err := plane.Deploy(ctx, strings.Replace(fillLadder, "RUNG step Start", "RUNG step /Start", 1), func(s string) { editLines <- s })
			edited <- err
		}()
		select {
		case l := <-editLines:
			if !strings.Contains(l, "waiting for the online session the download is opening") {
				t.Errorf("the waiting edit's first line = %q", l)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the waiting edit said nothing")
		}
		close(f.uploadHold)
		<-warmed
		// (The fake agent has no partial export, so the edit's own
		// verification fails; what matters is that it ran warm.)
		<-edited
		close(editLines)
		var got []string
		for l := range editLines {
			got = append(got, l)
		}
		if hasLine(got, "opening the online session") || !hasLine(got, "replacing Fill/MainRoutine online") {
			t.Errorf("edit lines = %q", got)
		}
		if !hasLine(warmLines, "session open and online") || hasLine(warmLines, "online edit live") {
			t.Errorf("warm-up lines = %q", warmLines)
		}
		return 0
	})
}

// A session held open by an online edit is closed before a download, and
// the download takes nautilus.yaml as it is now.
func TestLogixServePlaneReleasesTheSessionBeforeADownload(t *testing.T) {
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	f, url, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine
	f.mode = "Program" // a plain download to a controller in Run is refused up front (#391)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(url, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644)
	p, err := loadLogixProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() int {
		plane, release := logixProgramPlane(dir, p, logixd.New(url, ""))
		defer release()
		_, _ = plane.Deploy(context.Background(), fillLadder, func(string) {}) // opens a session
		// The manifest changes after the server started: the download
		// takes it as it is now.
		edited := strings.Replace(fillManifest(url, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n"),
			"{ name: Speed, role: setpoint, type: INT, init: 0 }",
			`{ name: Speed, role: setpoint, type: INT, init: 0, desc: "conveyor speed setpoint" }`, 1)
		if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		f.seen = nil
		_, _ = plane.Download(context.Background(), fillLadder, facade.DownloadOptions{}, func(string) {})
		closed := indexOf(f.seen, "DELETE /v1/sessions/")
		if closed < 0 || closed > indexOf(f.seen, "/download") {
			t.Errorf("the held session was not closed before the download: %v", f.seen)
		}
		sent := ""
		for name, raw := range f.files {
			if strings.HasSuffix(name, "/Line.L5X") && strings.HasPrefix(name, "deploy-") {
				sent += string(raw)
			}
		}
		if !strings.Contains(sent, "conveyor speed setpoint") {
			t.Error("the download did not take nautilus.yaml as it is now")
		}
		return 0
	})
}

// Stopping the serve closes the online session an edit opened, rather
// than leaving logixd to hold the project online until its idle reaper
// (seen on Echo, #375: every stopped serve left one).
func TestLogixServePlaneReleaseClosesTheSession(t *testing.T) {
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	f, url, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(url, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644)
	p, err := loadLogixProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() int {
		plane, release := logixProgramPlane(dir, p, logixd.New(url, ""))
		release() // nothing open: nothing to do
		if indexOf(f.seen, "DELETE /v1/sessions/") >= 0 {
			t.Errorf("release closed a session that was never opened: %v", f.seen)
		}
		_, _ = plane.Deploy(context.Background(), fillLadder, func(string) {}) // opens a session
		f.seen = nil
		release()
		if indexOf(f.seen, "/offline") < 0 || indexOf(f.seen, "DELETE /v1/sessions/") < 0 {
			t.Errorf("release did not take the session offline and close it: %v", f.seen)
		}
		f.seen = nil
		release() // once is enough
		if len(f.seen) != 0 {
			t.Errorf("a second release called the agent: %v", f.seen)
		}
		return 0
	})
}

func hasLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func indexOf(seen []string, sub string) int {
	for i, s := range seen {
		if strings.Contains(s, sub) {
			return i
		}
	}
	return -1
}

// A plain download with the controller in Run is refused before the
// import and build, with the StartStateError the editor's button flow
// keys on (#391); asking for Program mode goes ahead.
func TestLogixServePlaneRefusesADownloadInRunUpFront(t *testing.T) {
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	f, url, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine
	f.mode = "Run"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(url, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644)
	p, err := loadLogixProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() int {
		plane, release := logixProgramPlane(dir, p, logixd.New(url, ""))
		defer release()
		f.seen = nil
		_, err := plane.Download(context.Background(), fillLadder, facade.DownloadOptions{}, func(string) {})
		var se *deploy.StartStateError
		if !errors.As(err, &se) || se.Mode != "Run" {
			t.Fatalf("err = %v (%T)", err, err)
		}
		if indexOf(f.seen, "/build") >= 0 || indexOf(f.seen, "/download") >= 0 {
			t.Errorf("work after the refusal: %v", f.seen)
		}
		f.seen = nil
		if _, err := plane.Download(context.Background(), fillLadder, facade.DownloadOptions{ProgramMode: true}, func(string) {}); errors.As(err, &se) {
			t.Errorf("program mode was refused: %v", err)
		}
		if indexOf(f.seen, "/download") < 0 {
			t.Errorf("program mode did not download: %v", f.seen)
		}
		return 0
	})
}

// #1410: a download whose request is already gone still closes the
// held online session (on its own bounded time), not leaves the agent
// holding the project online for its reaper.
func TestLogixServePlaneDownloadClosesTheSessionPastACancelledRequest(t *testing.T) {
	t.Setenv("NAUTILUS_LOGIXD_URL", "")
	f, url, stop := newFakeLogixd(t)
	defer stop()
	f.controllerL5X = emptyLine
	f.mode = "Program"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(fillManifest(url, "    comm-path: AB_ETH-1\\10.0.0.5\\Backplane\\0\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "Fill.ld"), []byte(fillLadder), 0o644)
	p, err := loadLogixProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() int {
		plane, release := logixProgramPlane(dir, p, logixd.New(url, ""))
		defer release()
		_, _ = plane.Deploy(context.Background(), fillLadder, func(string) {}) // opens a session
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f.seen = nil
		_, _ = plane.Download(ctx, fillLadder, facade.DownloadOptions{}, func(string) {})
		if indexOf(f.seen, "DELETE /v1/sessions/") < 0 {
			t.Errorf("the held session was not closed for a cancelled request: %v", f.seen)
		}
		return 0
	})
}
