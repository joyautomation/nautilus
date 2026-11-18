//go:build js && wasm

// Run under Node: GOOS=js GOARCH=wasm go test -exec="$(go env GOROOT)/lib/wasm/go_js_wasm_exec" ./cmd/naut-wasm

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall/js"
	"testing"
	"time"
)

const sealinYAML = `name: sealin
tasks:
  - program: program.st
    scan: 50ms
tags:
  - { name: StartPB, role: setpoint, type: BOOL, init: false }
  - { name: Motor,   role: output,   type: BOOL, init: false }
`

const sealinST = `PROGRAM Main
Motor := StartPB OR Motor;
END_PROGRAM
`

func sealin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{"nautilus.yaml": sealinYAML, "program.st": sealinST} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stream opens /api/stream and returns a channel of its chunks, closed when
// the stream ends. A path alone, as every caller sends it (client.js,
// engine.ts through the bridge).
func stream(t *testing.T) (<-chan string, func()) {
	t.Helper()
	ch := make(chan string, 64)
	cancel := openStream("/api/stream", func(s string) {
		select {
		case ch <- s:
		default:
		}
	}, func() { close(ch) })
	return ch, cancel
}

func firstFrame(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case s, ok := <-ch:
		if !ok || !strings.HasPrefix(s, "data: ") {
			t.Fatalf("first chunk = %q, %v; want a frame", s, ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no frame")
	}
}

func ends(t *testing.T, ch <-chan string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("the stream was still open %v after its controller stopped", within)
		}
	}
}

// naut run on a running controller replaces it; the old controller's stream
// ends, so the client reconnects to the new one instead of waiting on silence.
func TestStreamEndsWhenRunReplacesTheController(t *testing.T) {
	dir := sealin(t)
	if _, err := load(dir); err != nil {
		t.Fatal(err)
	}
	defer stop()
	ch, cancel := stream(t)
	defer cancel()
	firstFrame(t, ch)
	if _, err := load(dir); err != nil {
		t.Fatal(err)
	}
	ends(t, ch, time.Second)

	// and a stream opened now reaches the new controller
	ch2, cancel2 := stream(t)
	defer cancel2()
	firstFrame(t, ch2)
}

func TestStreamEndsOnStop(t *testing.T) {
	if _, err := load(sealin(t)); err != nil {
		t.Fatal(err)
	}
	ch, cancel := stream(t)
	defer cancel()
	firstFrame(t, ch)
	stop()
	ends(t, ch, time.Second)
}

func TestStreamWithNothingRunningEndsAtOnce(t *testing.T) {
	stop()
	ch, cancel := stream(t)
	defer cancel()
	ends(t, ch, time.Second)
}

// #819: the controller answers the name the extension's web build sends
// every request to; if the two drift apart, every playground request is a
// 403.
func TestPlaygroundHostMatchesTheExtension(t *testing.T) {
	src, err := os.ReadFile("../../tools/vscode-iec/src/web/engine.ts")
	if err != nil {
		t.Fatal(err)
	}
	if want := `PLAYGROUND_URL = "http://` + playgroundHost + `"`; !strings.Contains(string(src), want) {
		t.Errorf("engine.ts lacks %s", want)
	}
}

// #819: a request the extension hands in (engine.ts sends the path alone,
// through the bridge to jsHandle) passes the controller's Host guard. When
// it was addressed to httptest's example.com, every one was a 403 and the
// live playground showed "nautilus: offline" for every example.
func TestHandleAnswersWhatTheExtensionSends(t *testing.T) {
	if _, err := load(sealin(t)); err != nil {
		t.Fatal(err)
	}
	defer stop()
	status := make(chan int, 1)
	then := js.FuncOf(func(_ js.Value, args []js.Value) any {
		status <- args[0].Get("status").Int()
		return nil
	})
	defer then.Release()
	headers := js.ValueOf(map[string]any{"accept": "application/json"})
	jsHandle(js.Null(), []js.Value{js.ValueOf("GET"), js.ValueOf("/api/state"), headers, js.Null()}).(js.Value).Call("then", then)
	select {
	case got := <-status:
		if got != 200 {
			t.Fatalf("GET /api/state as the extension sends it (its connect check): status %d, want 200", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer")
	}
}
