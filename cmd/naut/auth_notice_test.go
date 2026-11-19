package main

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/project"
)

// A controller with an auth: block says, loudly, what the block does not
// do yet; one without says nothing.
func TestAuthNotice(t *testing.T) {
	load := func(block string) *project.Project {
		p, err := project.Load(fstest.MapFS{
			"nautilus.yaml": &fstest.MapFile{Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n" + block)},
			"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if msg := authNotice(load("")); msg != "" {
		t.Errorf("no auth: block: %q", msg)
	}
	msg := authNotice(load("auth:\n  token-role: operator\n"))
	if !strings.Contains(msg, "WARNING") || !strings.Contains(msg, "NOT ENFORCED") || !strings.Contains(msg, "reads are not gated") {
		t.Errorf("auth: block: %q", msg)
	}
}
