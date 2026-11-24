//go:build js && wasm

package main

import (
	"os"
	"path/filepath"
	"syscall/js"
	"testing"
)

// TestBuildsAMimicScreen runs the builder module's own entry point, under
// Node, on a one-mimic project: the screen the playground builds for a
// project with *.mimic.json and no server.hmi.
func TestBuildsAMimicScreen(t *testing.T) {
	packs, err := os.ReadFile("../../internal/hmibuild/packs/packs.tar.gz")
	if err != nil {
		t.Skipf("no HMI packs (%v): run internal/hmibuild/packs/generate.sh", err)
	}
	// what hmi-worker.js defines: the pack, as a Promise of its bytes
	js.Global().Set("nautilusHMIPack", js.FuncOf(func(js.Value, []js.Value) any {
		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, p []js.Value) any {
			u8 := js.Global().Get("Uint8Array").New(len(packs))
			js.CopyBytesToJS(u8, packs)
			p[0].Invoke(u8)
			return nil
		}))
	}))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "nautilus.yaml"), []byte("name: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mimic := `{"version":1,"name":"tank","title":"Tank","width":400,"height":300,"nodes":[]}`
	if err := os.WriteFile(filepath.Join(root, "tank.mimic.json"), []byte(mimic), 0o644); err != nil {
		t.Fatal(err)
	}
	r := build(root, `{"app":"","title":"t"}`)
	if !r.Get("ok").Bool() {
		t.Fatalf("the build failed: %v", r.Get("messages"))
	}
	t.Logf("built in %v ms, %d files", r.Get("ms"), js.Global().Get("Object").Call("keys", r.Get("files")).Length())
	files := r.Get("files")
	if files.Get("index.html").Type() != js.TypeObject {
		t.Errorf("no index.html in the built screen: %v", js.Global().Get("Object").Call("keys", files))
	}
	if r.Get("pages").Length() == 0 {
		t.Error("the built screen lists no pages")
	}
}

// A build that fails answers {ok: false, messages}, not a thrown error.
func TestBadOptionsAreAnAnswer(t *testing.T) {
	r := build("/nowhere", "not json")
	if r.Get("ok").Bool() || r.Get("messages").Length() != 1 {
		t.Errorf("want {ok: false, one message}, got %v", r)
	}
}
