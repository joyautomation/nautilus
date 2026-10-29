package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// `naut eip import` writes the driver's <name>__Online status input into
// the tag file, named the way the loader will name it: from the eip driver
// in --out's nautilus.yaml whose host: matches, here a named drivers: entry.
// It says which name it used. The Go form puts eip.WithOnlineTag in its
// wiring hint.
func TestEIPImportWritesTheOnlineCompanion(t *testing.T) {
	host, port := startEIPEmulator(t)
	addr := host + ":" + strconv.Itoa(port)
	dir := t.TempDir()
	manifest := `tasks:
  - program: program.st
drivers:
  - type: eip
    host: 192.0.2.50
    manifest: other.yaml
  - type: eip
    name: line2
    host: ` + addr + `
    manifest: eip_manifest.yaml
tag-files: [tags/eip.yaml]
`
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun(t, func() int {
		return runEIP([]string{"import", "--host", host, "--port", strconv.Itoa(port), "--out", dir, "--format", "yaml"})
	})
	if code != 0 {
		t.Fatalf("import failed (%d):\n%s", code, out)
	}
	tags, err := os.ReadFile(filepath.Join(dir, "tags", "eip.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tags), "- { name: line2__Online, role: input, type: BOOL, desc: ") {
		t.Errorf("tag file lacks the companion:\n%s", tags)
	}
	if !strings.Contains(out, "online tag: line2__Online") {
		t.Errorf("the import did not say which name it used:\n%s", out)
	}

	// --name wins over the manifest, and is reduced to an identifier.
	out, code = captureRun(t, func() int {
		return runEIP([]string{"import", "--host", host, "--port", strconv.Itoa(port), "--out", dir,
			"--format", "go", "--name", "line-3"})
	})
	if code != 0 {
		t.Fatalf("import --name failed (%d):\n%s", code, out)
	}
	gosrc, err := os.ReadFile(filepath.Join(dir, "eip_manifest.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gosrc), `eip.WithOnlineTag("line_3__Online")`) {
		t.Errorf("Go wiring hint lacks WithOnlineTag:\n%s", gosrc)
	}
	if !strings.Contains(out, "online tag: line_3__Online (--name line-3)") {
		t.Errorf("output:\n%s", out)
	}
}

// Several eip drivers and none on this host: the import refuses before it
// dials anything, listing them and pointing at --name.
func TestEIPImportAmbiguousDriverNeedsName(t *testing.T) {
	dir := t.TempDir()
	manifest := `drivers:
  - { type: eip, host: 192.0.2.1, manifest: a.yaml }
  - { type: eip, host: 192.0.2.2, manifest: b.yaml }
`
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr, code := captureStderr(t, func() int {
		return runEIP([]string{"import", "--host", "192.0.2.99", "--out", dir, "--format", "yaml"})
	})
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	for _, want := range []string{"eip (host 192.0.2.1)", "eip-2 (host 192.0.2.2)", "--name"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("error lacks %q:\n%s", want, stderr)
		}
	}
}

// `naut eip tags` finds the driver by the manifest it was given, from the
// project in the working directory.
func TestEIPTagsResolvesTheCompanionByManifest(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	files := map[string]string{
		"nautilus.yaml":     "drivers:\n  - { type: modbus, manifest: m.yaml }\n  - { type: eip, name: press, host: 192.0.2.7, manifest: eip_manifest.yaml }\n",
		"eip_manifest.yaml": "tags:\n  - { name: Speed, device: Speed, type: REAL }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code := captureRun(t, func() int { return runEIP([]string{"tags", "eip_manifest.yaml"}) })
	if code != 0 {
		t.Fatalf("eip tags failed (%d):\n%s", code, out)
	}
	tags, err := os.ReadFile(filepath.Join("tags", "eip.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tags), "name: press__Online") {
		t.Errorf("tag file lacks press__Online:\n%s", tags)
	}
}
