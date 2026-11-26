//go:build js && wasm

package main

import "testing"

// A whole screen is built by playground/app/hmi-module.test.mjs: it
// needs the page's file system (Node's real one is far too slow to stand in: esbuild's
// stat storm took two minutes). Here: a build that fails answers {ok: false, messages}.
func TestBadOptionsAreAnAnswer(t *testing.T) {
	r := build("/nowhere", "not json")
	if r.Get("ok").Bool() || r.Get("messages").Length() != 1 {
		t.Errorf("want {ok: false, one message}, got %v", r)
	}
}
