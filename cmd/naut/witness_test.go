package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
)

func witnessEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// naut witness refuses to start without a peer token (test plan §8.2 case
// 7) or a state directory, and over a state file it can't read unless
// --rebuild.
const cluster = "0123456789abcdef0123456789abcdef"

func TestWitnessRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	var errb bytes.Buffer
	if code := witnessMain(context.Background(), []string{"--state", dir, "--listen", "127.0.0.1:0"}, witnessEnv(nil), &bytes.Buffer{}, &errb, nil); code != 1 || !strings.Contains(errb.String(), "NAUTILUS_PEER_TOKEN") {
		t.Fatalf("no token: exit %d, %q", code, errb.String())
	}
	errb.Reset()
	env := witnessEnv(map[string]string{"NAUTILUS_PEER_TOKEN": testPeerToken})
	if code := witnessMain(context.Background(), []string{"--listen", "127.0.0.1:0"}, env, &bytes.Buffer{}, &errb, nil); code != 1 || !strings.Contains(errb.String(), "state directory") {
		t.Fatalf("no state dir: exit %d, %q", code, errb.String())
	}
	good, _ := quorum.VoterState{Incarnation: 5}.MarshalBinary()
	if err := os.WriteFile(filepath.Join(dir, cluster+".quorum"), good[:len(good)-2], 0o600); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := witnessMain(context.Background(), []string{"--state", dir, "--listen", "127.0.0.1:0"}, env, &bytes.Buffer{}, &errb, nil); code != 1 || !strings.Contains(errb.String(), "--rebuild") {
		t.Fatalf("unreadable state: exit %d, %q", code, errb.String())
	}
}

// naut witness answers a vote that carries the peer token, and nothing
// without it; $STATE_DIRECTORY is its default state directory.
func TestWitnessServesVotes(t *testing.T) {
	dir := t.TempDir()
	env := witnessEnv(map[string]string{"NAUTILUS_PEER_TOKEN": testPeerToken, "STATE_DIRECTORY": dir})
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	exited := make(chan int, 1)
	go func() {
		exited <- witnessMain(ctx, []string{"--listen", "127.0.0.1:0", "--lease", "1s"}, env, &bytes.Buffer{}, &bytes.Buffer{}, ready)
	}()
	var addr string
	select {
	case addr = <-ready:
	case code := <-exited:
		t.Fatalf("exited %d", code)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	body, _ := quorum.Request{From: "a", Nonce: 3, ID: 1}.MarshalBinary()
	post := func(token string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, "https://"+addr+node.Prefix+cluster+"/vote", bytes.NewReader(body))
		if token != "" {
			req.Header.Set(node.TokenHeader, token)
		}
		req.Header.Set(node.LeaseHeader, strconv.FormatInt(int64(time.Second), 10))
		req.Header.Set(node.MembersHeader, "a,b")
		resp, err := peerClient(t, testPeerToken).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// TLS by default (#710): plain HTTP gets Go's 400 for it.
	if resp, err := http.Get("http://" + addr + node.Prefix + cluster + "/hello"); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain HTTP to the witness: %v %v, want 400 (peer TLS is on by default)", resp, err)
	} else {
		resp.Body.Close()
	}
	if r := post(""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without the token: %s", r.Status)
	}
	if r := post(testPeerToken); r.StatusCode != http.StatusOK {
		t.Fatalf("with the token: %s", r.Status)
	}
	// The witness saves a pair's state from a writer goroutine of its own, and
	// stopping the witness stops that writer: cancelling at once could beat
	// the first save (0.01 s, #1120). Wait for the file, then stop.
	file := filepath.Join(dir, cluster+".quorum")
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(file); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("no state file for the pair in $STATE_DIRECTORY after 5 s: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if code := <-exited; code != 0 {
		t.Fatalf("exit %d after stop", code)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the state file for the pair is gone after the stop: %v", err)
	}
}
