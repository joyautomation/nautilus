package project

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// benchFS is examples/plcnext-bench in memory, its nautilus.yaml with extra
// appended.
func benchFS(t *testing.T, extra string) fstest.MapFS {
	t.Helper()
	src := os.DirFS("../../examples/plcnext-bench")
	m := fstest.MapFS{}
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "hmi" || d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		m[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	y := strings.Replace(string(m["nautilus.yaml"].Data), "  hmi: hmi/build\n", "", 1)
	m["nautilus.yaml"] = &fstest.MapFile{Data: []byte(y + extra)}
	return m
}

// The plcnext driver flushes its output image from its own loop with no
// leadership check, so under redundancy: a standby would drive the
// controller's outputs. Load refuses the combination, as the driver or as
// one of drivers:, until #710 designs it; without redundancy: it loads.
func TestPlcnextRefusedUnderRedundancy(t *testing.T) {
	if _, err := Load(benchFS(t, ""), ""); err != nil {
		t.Fatalf("the bench project without redundancy: %v", err)
	}
	_, err := Load(benchFS(t, "redundancy:\n  lease: bench-lease\n"), "")
	if err == nil || !strings.Contains(err.Error(), "plcnext") || !strings.Contains(err.Error(), "#710") {
		t.Fatalf("got %v, want the plcnext-under-redundancy refusal naming #710", err)
	}

	// The same named in drivers: instead of driver:.
	m := benchFS(t, "")
	y := string(m["nautilus.yaml"].Data)
	y = strings.Replace(y, "driver:\n  type: plcnext\n  manifest: plcnext_manifest.yaml\n  scan-rate: 50ms\n",
		"drivers:\n  - type: plcnext\n    manifest: plcnext_manifest.yaml\n    scan-rate: 50ms\n", 1)
	if !strings.Contains(y, "drivers:\n") {
		t.Fatal("the bench manifest's driver block changed; update this test")
	}
	m["nautilus.yaml"] = &fstest.MapFile{Data: []byte(y + "redundancy:\n  lease: bench-lease\n")}
	if _, err := Load(m, ""); err == nil || !strings.Contains(err.Error(), "#710") {
		t.Fatalf("plcnext in drivers: under redundancy: got %v, want the #710 refusal", err)
	}
}
