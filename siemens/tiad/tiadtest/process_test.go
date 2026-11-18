package tiadtest_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	wd := "TIAD_WORKDIR=" + t.TempDir()
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"off loopback without a token", []string{wd, "TIAD_ADDR=http://0.0.0.0:0"}, "token is required"},
		{"any address without a token", []string{wd, "TIAD_ADDR=http://*:0"}, "TIAD_ADDR"},
		{"https", []string{wd, "TIAD_ADDR=https://127.0.0.1:0"}, "TIAD_ADDR"},
		{"a path", []string{wd, "TIAD_ADDR=http://127.0.0.1:0/tiad"}, "TIAD_ADDR"},
		{"user info", []string{wd, "TIAD_ADDR=http://u:p@127.0.0.1:0"}, "TIAD_ADDR"},
		{"target out of range", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=192.0.2.300"}, "TIAD_TARGETS"},
		{"target leading zero", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=192.0.2.10,01.2.3.4"}, "TIAD_TARGETS"},
		{"target short", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=10.1"}, "TIAD_TARGETS"},
		{"target hex", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=0x7f.0.0.1"}, "TIAD_TARGETS"},
		{"target ipv6", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=::1"}, "TIAD_TARGETS"},
		{"target name", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TARGETS=plc.local"}, "TIAD_TARGETS"},
		{"missing token file", []string{wd, "TIAD_ADDR=http://127.0.0.1:0", "TIAD_TOKEN_FILE=" + filepath.Join(t.TempDir(), "none")}, "TIAD_TOKEN_FILE"},
		{"no work dir off Windows", []string{"TIAD_ADDR=http://127.0.0.1:0"}, "TIAD_WORKDIR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out := tiadtest.RunToExit(t, bin, c.env...)
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
	p := tiadtest.StartProcess(t, bin, tiadtest.Config{}, "TIAD_TOKEN_FILE="+f)
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
	time.Sleep(200 * time.Millisecond) // the log line follows the reply
	log := p.Log()
	if !strings.Contains(log, "PUT /v1/files/f.txt 200") {
		t.Fatalf("the log does not name the call:\n%s", log)
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
	if !p.Exited(5 * time.Second) {
		t.Fatal("still running 5s after stdin closed")
	}
}
