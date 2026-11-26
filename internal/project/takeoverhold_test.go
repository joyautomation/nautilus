//go:build !js

package project

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// redundancy.takeover-hold (#710 (b2)): its fields, its tags and its
// drivers are checked at load, so naut check refuses what naut run would.

const holdTags = `tags:
  - {name: Go, role: state, init: false}
  - {name: SP, role: output, init: 0}
  - {name: Kept, role: state, init: false, retain: true}
  - {name: Level, role: setpoint, init: 1.5}
  - {name: Count, role: state, init: 0}
`

func holdFS(hold string) fstest.MapFS {
	fs := pairFS(holdTags + "driver:\n  type: modbus\n  manifest: modbus_manifest.yaml\n" + pairOK + hold)
	fs["modbus_manifest.yaml"] = &fstest.MapFile{Data: []byte("sources:\n" +
		"  - {id: W, host: 192.0.2.1}\n" +
		"tags:\n" +
		"  - {name: SP, source: W, table: holding, address: 0, format: uint16, writable: true}\n")}
	return fs
}

// A hold loads with its defaults (max 5 s, cold, stay held), and the
// runtime gets it as written.
func TestTakeoverHoldLoads(t *testing.T) {
	p, err := Load(holdFS("  takeover-hold:\n    release: Go\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	h := p.Redundancy.RuntimeTakeoverHold()
	if h == nil || h.Release != "Go" || h.Max != 5*time.Second || h.Always || h.ReleaseAtMax || len(h.Safe) != 0 {
		t.Fatalf("defaults: %+v", h)
	}
	p, err = Load(holdFS("  takeover-hold:\n    release: go\n    max: 30s\n    on: always\n    on-max: release\n    safe: {SP: 0}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	h = p.Redundancy.RuntimeTakeoverHold()
	if h.Max != 30*time.Second || !h.Always || !h.ReleaseAtMax || len(h.Safe) != 1 {
		t.Fatalf("set: %+v", h)
	}
	if (*RedundancyConfig)(nil).RuntimeTakeoverHold() != nil {
		t.Fatal("no redundancy: a hold")
	}
}

// What a hold must not be, refused with the reason.
func TestTakeoverHoldRefusals(t *testing.T) {
	for _, c := range []struct{ name, hold, want string }{
		{"no release tag", "  takeover-hold:\n    max: 5s\n", "release is required"},
		{"max over the cap", "  takeover-hold:\n    release: Go\n    max: 2m\n", "at most 1m"},
		{"max zero", "  takeover-hold:\n    release: Go\n    max: 0s\n", "more than 0"},
		{"on unknown", "  takeover-hold:\n    release: Go\n    on: hot\n", "want cold"},
		{"on-max unknown", "  takeover-hold:\n    release: Go\n    on-max: alarm\n", "want hold"},
		{"release tag unknown", "  takeover-hold:\n    release: Nope\n", "no such tag"},
		{"release tag an output", "  takeover-hold:\n    release: SP\n", `role "output"`},
		{"release tag a setpoint", "  takeover-hold:\n    release: Level\n", `role "setpoint"`},
		{"release tag retained", "  takeover-hold:\n    release: Kept\n", "is retained"},
		{"release tag not BOOL", "  takeover-hold:\n    release: Count\n", "want a BOOL tag"},
		{"safe on a state tag", "  takeover-hold:\n    release: Go\n    safe: {Count: 0}\n", "not an output tag"},
		{"safe not a value", "  takeover-hold:\n    release: Go\n    safe: {SP: low}\n", "not a BOOL or a number"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(holdFS(c.hold), "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
	if _, err := Load(pairFS("redundancy:\n  takeover-hold:\n    release: Go\n"), ""); err == nil || !strings.Contains(err.Error(), "applies to a redundant pair") {
		t.Fatalf("a hold without peers: %v", err)
	}
}

// plainOut is a driver that cannot write safe values; safeOut one that can.
type plainOut struct{ nio.Memory }

func (*plainOut) InputNames() []string  { return []string{"In"} }
func (*plainOut) OutputNames() []string { return []string{"Valve", "Bound"} }

type safeOut struct{ plainOut }

func (*safeOut) WriteSafe(nio.Values) error { return nil }
func (*safeOut) SafeUndelivered() []string  { return nil }

// A safe value for an output whose driver cannot write it is refused,
// naming the driver as our gap; a release tag a driver binds is refused.
func TestTakeoverHoldDriverChecks(t *testing.T) {
	tags := []TagConfig{{Name: "Go", Role: "state", Init: false}, {Name: "Valve", Role: "output"}, {Name: "Bound", Role: "state", Init: false}}
	h := &TakeoverHoldConfig{Release: "Go", Safe: map[string]any{"Valve": 0}}
	err := checkTakeoverHold(h, tags, []nio.NamedDriver{{Name: "line1", Driver: &plainOut{}}})
	if err == nil || !strings.Contains(err.Error(), "safe values are not yet written for project") || !strings.Contains(err.Error(), `"line1"`) {
		t.Fatalf("a safe value on a driver without WriteSafe: %v", err)
	}
	if err := checkTakeoverHold(h, tags, []nio.NamedDriver{{Name: "line1", Driver: &safeOut{}}}); err != nil {
		t.Fatalf("a safe value on a SafeWriter: %v", err)
	}
	if err := checkTakeoverHold(h, tags, nil); err == nil || !strings.Contains(err.Error(), "no driver writes") {
		t.Fatalf("a safe value with no driver: %v", err)
	}
	bound := &TakeoverHoldConfig{Release: "Bound"}
	if err := checkTakeoverHold(bound, tags, []nio.NamedDriver{{Name: "line1", Driver: &safeOut{}}}); err == nil || !strings.Contains(err.Error(), "bound to driver") {
		t.Fatalf("a release tag a driver binds: %v", err)
	}
}
