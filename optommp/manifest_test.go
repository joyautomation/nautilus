package optommp

import (
	"strings"
	"testing"
	"time"
)

func TestManifestRoundTrip(t *testing.T) {
	dev := newBenchDevice()
	o := ManifestOptions{ID: "EPIC", Host: "192.0.2.10", ScanRate: 20 * time.Millisecond, Latches: true, Quality: true, ScratchFloats: 2}
	m := benchManifest(t, dev, o)
	cmd := ManifestCommand(ManifestOptions{ID: "EPIC", Host: "192.0.2.10", ScanRate: 20 * time.Millisecond, Latches: true, Quality: true, ScratchFloats: 2})
	if cmd != "naut optommp import --host 192.0.2.10 --scan-rate 20ms --latches --quality --scratch-floats 2" {
		t.Errorf("command: %s", cmd)
	}
	raw := ManifestYAML(m, cmd)
	back, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if again := ManifestYAML(back, cmd); string(again) != string(raw) {
		t.Errorf("not byte-identical after a round trip:\n%s\n---\n%s", raw, again)
	}
	if time.Duration(back.ScanRate) != 20*time.Millisecond || back.Port != DefaultPort || len(back.Modules) != 4 {
		t.Errorf("parsed: rate %s port %d modules %d", time.Duration(back.ScanRate), back.Port, len(back.Modules))
	}
	if back.Modules[2].Type != 0x6000001F || back.Modules[2].Channels != 12 {
		t.Errorf("slot 2: %+v", back.Modules[2])
	}
	// 24 DI (+2 latches each) + 12 DO + 12+24 AI (+1 quality each) + 2 scratch floats
	if n := len(back.Tags); n != 24*3+12+36*2+2 {
		t.Errorf("tags: %d", n)
	}
	for _, want := range []string{
		`  - { slot: 3, part: "GRV-IV-24", type: 0x60000002, channels: 24 }`,
		`  - { name: EPIC_M1_DO11, point: do, slot: 1, channel: 11, type: "Digital Output" }`,
		`  - { name: EPIC_M2_AI00, point: ai, slot: 2, channel: 0, type: "ICTD Temperature Probe", unit: "°C", lo: -270, hi: 150 }`,
		`  - { name: EPIC_M0_DI04_OnLatch, point: on-latch, slot: 0, channel: 4 }`,
		`  - { name: EPIC_M3_AI00_Quality, point: quality, slot: 3, channel: 0 }`,
		`  - { name: EPIC_SP_F1, point: scratch-float, index: 1, writable: true }`,
	} {
		if !strings.Contains(string(raw), want+"\n") {
			t.Errorf("manifest lacks\n%s\n%s", want, raw)
		}
	}
	if got := back.OutputNames(); len(got) != 12+2 || got[0] != "EPIC_M1_DO00" {
		t.Errorf("outputs: %v", got)
	}

	tags, err := ManifestTagsYAML(back, cmd, "tags/optommp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`- { name: EPIC_M1_DO11, role: output, init: false, desc: "GRV-ODCI-12 slot 1 ch 11 — Digital Output" }`,
		`- { name: EPIC_M2_AI00, role: input, unit: "°C", desc: "GRV-IICTD-12 slot 2 ch 0 — ICTD Temperature Probe (-270 to 150)" }`,
		`- { name: EPIC_M0_DI04_OnLatch, role: input, desc: "GRV-IACDCTTL-24 slot 0 ch 4 — turned on since the previous scan" }`,
		`- { name: EPIC_SP_F1, role: output, init: 0.0, desc: "scratchpad float 1" }`,
	} {
		if !strings.Contains(string(tags), want+"\n") {
			t.Errorf("tag file lacks\n%s\n%s", want, tags)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	_, err := ParseManifest([]byte(`
port: 70000
scan-rate: 100us
modules:
  - { slot: 0, part: X, type: 0x50000005, channels: 24 }
  - { slot: 0, part: Y, type: 0x50000005, channels: 24 }
tags:
  - { name: A, point: di, slot: 16, channel: 0 }
  - { name: A, point: di, slot: 0, channel: 0 }
  - { name: B, point: ai, slot: 0, channel: 32 }
  - { name: C, point: di, slot: 0, channel: 1, writable: true }
  - { name: D, point: scratch-bit, index: 64 }
  - { name: E, point: analog }
  - { name: F }
`))
	if err == nil {
		t.Fatal("accepted a broken manifest")
	}
	for _, want := range []string{
		"host is required", "port 70000", "scan-rate 100µs", "module slot 0 listed twice",
		"tag A: slot 16", `duplicate tag name "A"`, "tag B: channel 32", "tag C: a di point is read-only",
		"tag D: index 64 (want 0..63)", `tag E: point "analog"`, "tag F: point is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if _, err := ParseManifest([]byte("host: h\nbogus: 1\ntags: []\n")); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("unknown key: %v", err)
	}
	if _, err := ParseManifest([]byte("host: h\nmodules:\n  - { slot: 0, type: nope }\ntags:\n  - { name: A, point: di }\n")); err == nil || !strings.Contains(err.Error(), "type code") {
		t.Errorf("bad type code: %v", err)
	}
}
