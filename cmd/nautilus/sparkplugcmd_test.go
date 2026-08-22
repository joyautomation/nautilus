package main

// The generator itself is tested against golden files in
// sparkplug/host/codegen. What is left to pin here is the COMMAND: that the
// offline path writes the three files a project needs, in the places the
// project loader looks for them, and that `nautilus sparkplug tags` can
// re-derive the tag file from the committed manifest alone — no broker, which
// is what makes it usable in CI and during review.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSites = `
group: PomonaWRD
types:
  - name: Motor
    fields:
      - {name: Speed, type: Double}
      - {name: Run, type: Boolean}
sites:
  - node: W6
    metrics:
      - {name: Well/Level, type: Double}
      - {name: Pump1, type: Motor}
    devices:
      - device: PLC1
        metrics:
          - {name: Pump/Run, type: Boolean}
          - {name: Pump/SpeedSP, type: Double, writable: true, init: 0.0}
`

func TestSparkplugImportFromSites(t *testing.T) {
	dir := t.TempDir()
	sites := filepath.Join(dir, "sites.yaml")
	if err := os.WriteFile(sites, []byte(sampleSites), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runSparkplug([]string{"import", "--sites", sites, "--out", dir}); code != 0 {
		t.Fatalf("import failed (%d)", code)
	}

	// sparkplug_types.st must land in the project ROOT: project.go treats
	// every root-level .st without a PROGRAM as a library, which is how the
	// generated TYPEs become visible to `type:` references.
	types := read(t, filepath.Join(dir, "sparkplug_types.st"))
	if !strings.Contains(types, "Motor : STRUCT") {
		t.Errorf("types file missing the Motor TYPE:\n%s", types)
	}
	manifest := read(t, filepath.Join(dir, "sparkplug_manifest.yaml"))
	for _, want := range []string{"group: PomonaWRD", "edgenode: W6", "metric: Pump/SpeedSP", "writable: true"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %q:\n%s", want, manifest)
		}
	}
	tags := read(t, filepath.Join(dir, "tags", "sparkplug.yaml"))
	for _, want := range []string{
		"- { name: W6_PLC1_Pump_SpeedSP, role: output, init: 0.0 }",
		"- { name: W6_PLC1__Online, role: input }",
		"- { name: W6_Pump1, role: input, type: Motor }",
		"- { name: W6__Rebirth, role: output, init: false }",
	} {
		if !strings.Contains(tags, want) {
			t.Errorf("tag file missing:\n  %s\ngot:\n%s", want, tags)
		}
	}

	// `nautilus sparkplug tags` must reproduce the same file from the
	// manifest alone.
	out := filepath.Join(dir, "again.yaml")
	if code := runSparkplug([]string{"tags", "--out", out, filepath.Join(dir, "sparkplug_manifest.yaml")}); code != 0 {
		t.Fatalf("tags failed (%d)", code)
	}
	if again := read(t, out); again != tags {
		t.Errorf("re-derived tag file differs:\n%s", again)
	}
}

func TestSparkplugTagsSkipMustMatch(t *testing.T) {
	dir := t.TempDir()
	sites := filepath.Join(dir, "sites.yaml")
	if err := os.WriteFile(sites, []byte(sampleSites), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runSparkplug([]string{"import", "--sites", sites, "--out", dir}); code != 0 {
		t.Fatalf("import failed (%d)", code)
	}
	manifest := filepath.Join(dir, "sparkplug_manifest.yaml")
	if code := runSparkplug([]string{"tags", "--out", filepath.Join(dir, "t.yaml"),
		"--skip", "W6__Rebirth", manifest}); code != 0 {
		t.Fatalf("skipping a real tag should succeed (%d)", code)
	}
	// A stale exclusion silently regenerates a tag the project declares by
	// hand, colliding with it at compose time. Fail loud instead.
	if code := runSparkplug([]string{"tags", "--out", filepath.Join(dir, "t2.yaml"),
		"--skip", "W9_*", manifest}); code == 0 {
		t.Fatal("a skip pattern matching nothing must fail")
	}
}

func TestSparkplugImportRequiresBrokerOrSites(t *testing.T) {
	if code := runSparkplug([]string{"import"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if code := runSparkplug([]string{"nonsense"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
