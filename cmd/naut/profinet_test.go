package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfinetGSDMLAndTags(t *testing.T) {
	dir := t.TempDir()
	m := filepath.Join(dir, "profinet_device.yaml")
	os.WriteFile(m, []byte(`interface: eth1
station: naut-1
vendor-id: 0x1234
device-id: 0x0001
slots:
  - slot: 1
    module-ident: 0x00000101
    subslots:
      - {subslot: 1, submodule-ident: 0x00000001, inputs: 4, outputs: 1}
tags:
  - {name: Level, slot: 1, subslot: 1, dir: input,  offset: 0, format: float32}
  - {name: Fill,  slot: 1, subslot: 1, dir: output, offset: 0, bit: 0, format: bool}
`), 0o644)
	if code := runProfinet([]string{"gsdml", "-o", dir, "--date", "20261117", m}); code != 0 {
		t.Fatalf("gsdml exit %d", code)
	}
	x, err := os.ReadFile(filepath.Join(dir, "GSDML-V2.35-nautilus-naut-1-20261117.xml"))
	if err != nil || !strings.Contains(string(x), `DNS_CompatibleName="naut-1"`) {
		t.Fatalf("gsdml: %v", err)
	}
	tags := filepath.Join(dir, "tags", "profinet.yaml")
	if code := runProfinet([]string{"tags", "-o", tags, m}); code != 0 {
		t.Fatalf("tags exit %d", code)
	}
	b, _ := os.ReadFile(tags)
	for _, want := range []string{"name: Fill, role: input, type: BOOL", "name: Level, role: output, type: REAL", "profinet_device__Online"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("tag file lacks %q:\n%s", want, b)
		}
	}
	if code := runProfinet([]string{"gsdml", filepath.Join(dir, "missing.yaml")}); code == 0 {
		t.Fatal("missing manifest accepted")
	}
}
