package tiadtest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

var ctx = context.Background()

// These tests need a real process: the agent's own configuration, its
// file system and its log. They run against tiad-fake, which is the C#
// agent's core.

func TestAgentRefusesUnsafeConfig(t *testing.T) {
	bin := fakeBin(t)
	work := t.TempDir()
	wd, tok := "TIAD_WORKDIR="+work, "TIAD_TOKEN=t"
	// Every case gets a pins dir of its own unless it names one.
	pd := "TIAD_PINS_DIR=" + t.TempDir()
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"no token on loopback", []string{wd, "TIAD_ADDR=http://127.0.0.1:0"}, "token is required"},
		{"no token off loopback", []string{wd, "TIAD_ADDR=http://192.0.2.1:0"}, "token is required"},
		{"every interface", []string{wd, tok, "TIAD_ADDR=http://0.0.0.0:0"}, "one IPv4 address"},
		{"a name", []string{wd, tok, "TIAD_ADDR=http://localhost:0"}, "one IPv4 address"},
		{"a blank token", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TOKEN=  "}, "token is required"},
		{"an empty token file", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TOKEN_FILE=" + emptyFile(t)}, "is empty"},
		{"any address", []string{wd, tok, "TIAD_ADDR=http://*:0"}, "TIAD_ADDR"},
		{"https", []string{wd, tok, "TIAD_ADDR=https://127.0.0.1:0"}, "TIAD_ADDR"},
		{"a path", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0/tiad"}, "TIAD_ADDR"},
		{"user info", []string{wd, tok, "TIAD_ADDR=http://u:p@127.0.0.1:0"}, "TIAD_ADDR"},
		{"target out of range", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=192.0.2.300"}, "TIAD_TARGETS"},
		{"target leading zero", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=192.0.2.10,01.2.3.4"}, "TIAD_TARGETS"},
		{"target short", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=10.1"}, "TIAD_TARGETS"},
		{"target hex", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=0x7f.0.0.1"}, "TIAD_TARGETS"},
		{"target ipv6", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=::1"}, "TIAD_TARGETS"},
		{"target name", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=plc.local"}, "TIAD_TARGETS"},
		{"missing token file", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TOKEN_FILE=" + filepath.Join(t.TempDir(), "none")}, "TIAD_TOKEN_FILE"},
		{"no work dir off Windows", []string{tok, "TIAD_ADDR=http://127.0.0.1:0"}, "TIAD_WORKDIR"},
		{"file cap not a number", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_MAX_FILE_BYTES=1e9"}, "TIAD_MAX_FILE_BYTES"},
		{"idle not a duration", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_SESSION_IDLE=60"}, "TIAD_SESSION_IDLE"},
		{"idle too short", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_SESSION_IDLE=1us"}, "TIAD_SESSION_IDLE"},
		{"retire watch not a duration", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_RETIRE_WATCH=60"}, "TIAD_RETIRE_WATCH"},
		{"retire watch too long", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_RETIRE_WATCH=1h1s"}, "TIAD_RETIRE_WATCH"},
		{"file cap zero", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_MAX_FILE_BYTES=0"}, "TIAD_MAX_FILE_BYTES"},
		{"no-TIA switch not 1", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_NO_TIA=true"}, "TIAD_NO_TIA must be 1 or unset"},
		{"stdin switch not 1", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_EXIT_ON_STDIN=yes"}, "TIAD_EXIT_ON_STDIN must be 1 or unset"},
		// The files API writes anywhere in the work dir: a pin must never be
		// within its reach.
		{"no pins dir off Windows", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_PINS_DIR="}, "TIAD_PINS_DIR is required"},
		{"pins dir in the work dir", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_PINS_DIR=" + filepath.Join(work, "pins")}, "TIAD_PINS_DIR must be outside TIAD_WORKDIR"},
		{"pins dir is the work dir", []string{wd, tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_PINS_DIR=" + work + "/"}, "TIAD_PINS_DIR must be outside TIAD_WORKDIR"},
		{"work dir in the pins dir", []string{"TIAD_WORKDIR=" + filepath.Join(filepath.Dir(work), "w"), tok, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_PINS_DIR=" + filepath.Dir(work)}, "TIAD_PINS_DIR must be outside TIAD_WORKDIR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := append([]string{pd}, c.env...) // a case's own TIAD_PINS_DIR wins
			code, out := tiadtest.RunToExit(t, bin, env...)
			if code != 2 || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, want 2 with %q in:\n%s", code, c.want, out)
			}
		})
	}
}

func TestAgentTokenFile(t *testing.T) {
	bin := fakeBin(t)
	f := filepath.Join(t.TempDir(), "tiad.token")
	if err := os.WriteFile(f, []byte("from-the-file\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := tiadtest.StartProcess(t, bin, tiadtest.Config{}, "TIAD_TOKEN=", "TIAD_TOKEN_FILE="+f)
	if _, err := tiad.New(p.URL, "from-the-file").Health(ctx); err != nil {
		t.Fatalf("the file's token: %v", err)
	}
	if _, err := tiad.New(p.URL, "wrong").Health(ctx); tiad.Kind(err) != tiad.KindUnauthorized {
		t.Fatalf("a wrong token: %v", err)
	}
}

// TestAgentNeverFollowsALink: a link inside the work dir could point
// anywhere, so no request goes through one.
func TestAgentNeverFollowsALink(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"dirlink":  outside,
		"filelink": filepath.Join(outside, "secret.txt"),
		"dangling": filepath.Join(outside, "made-through-a-link.txt"),
	} {
		if err := os.Symlink(target, filepath.Join(p.WorkDir, name)); err != nil {
			t.Skipf("symlink: %v", err)
		}
	}

	for _, path := range []string{"dirlink/secret.txt", "filelink"} {
		if b, err := c.GetFile(ctx, path); err == nil || bytes.Contains(b, []byte("outside")) {
			t.Errorf("GET %s through a link = %q %v", path, b, err)
		}
		if err := c.DeleteFile(ctx, path); tiad.Kind(err) != tiad.KindBadRequest {
			t.Errorf("DELETE %s through a link = %v, want bad_request", path, err)
		}
	}
	if _, err := c.PutFile(ctx, "dirlink/new.txt", []byte("x")); err == nil {
		t.Error("PUT through a directory link succeeded")
	}
	// Writing at a dangling link replaces the link; it never creates the
	// file the link points at.
	_, _ = c.PutFile(ctx, "dangling", []byte("x"))
	if _, err := os.Stat(filepath.Join(outside, "made-through-a-link.txt")); err == nil {
		t.Error("PUT at a dangling link wrote outside the work dir")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "outside" {
		t.Errorf("the file outside changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("PUT wrote outside the work dir")
	}
	for _, f := range files(t, c) {
		if strings.Contains(f.Path, "link") || strings.Contains(f.Path, "secret") {
			t.Errorf("list shows %s through a link", f.Path)
		}
	}
}

func emptyFile(t *testing.T) string {
	f := filepath.Join(t.TempDir(), "empty.token")
	if err := os.WriteFile(f, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// rawPost posts body as JSON past the client's own checks, so the agent
// itself decides.
func rawPost(t *testing.T, p *tiadtest.Process, rawPath string, body any) (int, string) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, p.URL+rawPath, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+p.Client().Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func put(t *testing.T, p *tiadtest.Process, rawPath string, body []byte) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, p.URL+rawPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.Client().Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestAgentKeepsAbsolutePathsOut: an escaped '/' makes a segment that is
// an absolute path. The lexical rule refuses it, and the full-path
// containment check refuses it again if the lexical rule ever lets it by.
func TestAgentKeepsAbsolutePathsOut(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	outside := t.TempDir()
	for _, target := range []string{filepath.Join(outside, "abs.txt"), filepath.Join(outside, "sub", "abs.txt")} {
		status, body := put(t, p, "/v1/files/"+url.PathEscape(target), []byte("x"))
		if status != http.StatusBadRequest {
			t.Errorf("PUT %s = HTTP %d %s, want 400", target, status, body)
		}
		if _, err := os.Stat(target); err == nil {
			t.Errorf("PUT wrote %s, outside the work dir", target)
		}
	}
}

func TestAgentCapsFileSize(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{}, "TIAD_MAX_FILE_BYTES=1000")
	c := p.Client()
	if _, err := c.PutFile(ctx, "fits.bin", make([]byte, 1000)); err != nil {
		t.Fatalf("a file at the cap: %v", err)
	}
	_, err := c.PutFile(ctx, "big.bin", make([]byte, 1001))
	var te *tiad.Error
	if !errors.As(err, &te) || te.Kind != tiad.KindBadRequest || te.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a file over the cap: %v, want 413 bad_request", err)
	}
	if fs := files(t, c); len(fs) != 1 || fs[0].Path != "fits.bin" {
		t.Fatalf("files = %+v, want only fits.bin", fs)
	}
}

// TestAgentRefusesAnUnusableName: a name the file system cannot hold is a
// bad request, not an agent failure.
func TestAgentRefusesAnUnusableName(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	long := strings.Repeat("a", 300)
	if _, err := c.PutFile(ctx, long, []byte("x")); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("PUT a 300-character name: %v, want bad_request", err)
	}
	if _, err := c.GetFile(ctx, "d/"+long); tiad.Kind(err) != tiad.KindBadRequest && tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("GET a 300-character name: %v", err)
	}
}

// TestAgentReplacesFilesAtomically: puts of one path race each other
// without failing, and a reader never finds the file missing.
func TestAgentReplacesFilesAtomically(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	if _, err := c.PutFile(ctx, "hot.txt", []byte("v0")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for w := 0; w < 8; w++ {
		wg.Add(2)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := c.PutFile(ctx, "hot.txt", []byte(fmt.Sprintf("v%d-%d", w, i))); err != nil {
					errs <- fmt.Errorf("put: %w", err)
				}
			}
		}(w)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if b, err := c.GetFile(ctx, "hot.txt"); err != nil || !bytes.HasPrefix(b, []byte("v")) {
					errs <- fmt.Errorf("get: %q %w", b, err)
				}
			}
		}()
	}
	// The first put of a new path, many at once: one wins, none fails.
	for r := 0; r < 20; r++ {
		name := fmt.Sprintf("fresh-%d.txt", r)
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				if _, err := c.PutFile(ctx, name, []byte(fmt.Sprintf("w%d", w))); err != nil {
					errs <- fmt.Errorf("first put of %s: %w", name, err)
				}
			}(w)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestAgentJSONNeedsItsContentType: a JSON body is read only as
// application/json (here through tiad-fake's control call, which reads its
// body the way every JSON call of the agent does).
func TestAgentJSONNeedsItsContentType(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	body := []byte(`{"gates":[{"name":"licence","ok":false}]}`)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/octet-stream"} {
		req, _ := http.NewRequest(http.MethodPut, p.URL+"/fake/gates", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+p.Client().Token)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("JSON body as %q = HTTP %d, want 415", ct, resp.StatusCode)
		}
	}
	if pr, err := p.Client().Probe(ctx); err != nil || !pr.Usable {
		t.Fatalf("a refused body changed the gates: %+v %v", pr, err)
	}
}

func files(t *testing.T, c *tiad.Client) []tiad.FileInfo {
	t.Helper()
	fs, err := c.ListFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// TestAgentLogsNoSecret: the log names calls, never a token or a body.
func TestAgentLogsNoSecret(t *testing.T) {
	const token = "token-must-not-be-logged"
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{Token: token})
	c := p.Client()
	if _, err := c.PutFile(ctx, "f.txt", []byte("body-must-not-be-logged")); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, p.URL+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer wrong-token-must-not-be-logged")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	// A call's log line follows its reply: wait for the last one.
	var log string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		log = p.Log()
		if strings.Contains(log, "GET /v1/health 401") || time.Now().After(deadline) {
			break
		}
	}
	if !strings.Contains(log, "PUT /v1/files/f.txt 200") || !strings.Contains(log, "GET /v1/health 401") {
		t.Fatalf("the log does not name the calls:\n%s", log)
	}
	if strings.Contains(log, "must-not-be-logged") {
		t.Fatalf("the log carries a secret:\n%s", log)
	}
}

func TestAgentExitsWhenStdinCloses(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	if p.Exited(300 * time.Millisecond) {
		t.Fatal("exited while stdin was open")
	}
	p.CloseStdin()
	if !p.Exited(10 * time.Second) {
		t.Fatal("still running 10s after stdin closed")
	}
}
