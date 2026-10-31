package main

// The generator's semantics are tested in opcua/codegen. What is pinned
// here is the COMMAND: that `naut opcua tags` re-derives a tag file from a
// committed manifest with no server, that the file names the companion the
// driver delivers, and that the manifest it read loads through the core's
// strict decoder.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const opcuaSampleManifest = `namespaces: ["http://opcfoundation.org/UA/", "urn:ignition"]
tags:
  - {name: GW_Memory, nodeid: "ns=1;s=[System]Gateway/Performance/Memory Utilization", namespace: "urn:ignition", type: Double, desc: "heap in use, percent"}
  - {name: GW_Setpoint, nodeid: "ns=1;s=[default]Setpoint", namespace: "urn:ignition", type: Int32, writable: true}
  - {name: GW_Handled, nodeid: "ns=1;s=[default]Handled", namespace: "urn:ignition", type: Boolean}
`

func TestOPCUATagsCommand(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "opcua_manifest.yaml")
	if err := os.WriteFile(manifest, []byte(opcuaSampleManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "tags", "opcua.yaml")
	if code := runOPCUA([]string{"tags", "-o", out, "--name", "gateway", "--skip", "GW_Handled", manifest}); code != 0 {
		t.Fatalf("naut opcua tags exited %d", code)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`- { name: GW_Memory, role: input, desc: "heap in use, percent" }`,
		`- { name: GW_Setpoint, role: output }`,
		`- { name: gateway__Online, role: input, desc: "true while the OPC UA session is up (driver companion)" }`,
		"Not generated (declared by hand in the project): GW_Handled",
	}
	for _, w := range want {
		if !strings.Contains(string(got), w) {
			t.Errorf("tag file lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(string(got), "GW_Handled, role") {
		t.Errorf("a skipped tag was generated:\n%s", got)
	}
	// A stale skip pattern is an error, not a silent regeneration.
	if code := runOPCUA([]string{"tags", "-o", out, "--skip", "Nope_*", manifest}); code == 0 {
		t.Error("a skip pattern matching nothing was accepted")
	}
}

func TestOPCUAUsage(t *testing.T) {
	if code := runOPCUA(nil); code != 2 {
		t.Errorf("no subcommand exited %d, want 2", code)
	}
	if code := runOPCUA([]string{"import"}); code != 1 {
		t.Errorf("import without --endpoint exited %d, want 1", code)
	}
}

// `naut check` holds an opcua driver to its required keys and its security
// spellings, offline, and warns (without failing) about a username with no
// password in the environment.
func TestCheckOPCUAClientDriver(t *testing.T) {
	t.Setenv("NAUTILUS_OPCUA_PASSWORD", "")
	manifest := "namespaces: [\"http://opcfoundation.org/UA/\", \"urn:x\"]\ntags:\n" +
		"  - {name: Sensor, nodeid: \"ns=1;s=Sensor\", namespace: \"urn:x\", type: Double}\n"
	driver := func(extra string) string {
		return checkManifestYAML + "driver:\n  type: opcua\n  manifest: opcua_manifest.yaml\n" + extra
	}
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml":       driver("  endpoint: opc.tcp://192.0.2.1:4840\n  security: None\n  username: op\n"),
		"opcua_manifest.yaml": manifest,
		"program.st":          programBinding("", ""),
	})
	if code != 0 {
		t.Fatalf("check refused a complete opcua driver:\n%s", out)
	}
	if !strings.Contains(out, "NAUTILUS_OPCUA_PASSWORD") {
		t.Errorf("check did not warn about the missing password:\n%s", out)
	}
	for name, extra := range map[string]string{
		"no endpoint": "",
		"bad policy":  "  endpoint: opc.tcp://192.0.2.1:4840\n  security: Basic256Sha265\n",
		"bad mode":    "  endpoint: opc.tcp://192.0.2.1:4840\n  security-mode: Encrypt\n",
	} {
		out, code := checkIn(t, map[string]string{
			"nautilus.yaml":       driver(extra),
			"opcua_manifest.yaml": manifest,
			"program.st":          programBinding("", ""),
		})
		if code == 0 {
			t.Errorf("%s: check passed:\n%s", name, out)
		}
	}
}
