package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A manifest whose tags cannot be read (an unknown role, a setpoint with
// neither init nor type) is what naut check reports, once, on the
// manifest. Before, the programs were compiled against no tags at all and
// check said only that the first tag they used was undeclared, naming a
// valid tag and never the cause.
func TestCheckReportsAManifestTagErrorNotUndeclaredTags(t *testing.T) {
	for name, bad := range map[string]string{
		"unknown role":          `{ name: X, role: command, type: BOOL }`,
		"setpoint with no type": `{ name: X, role: setpoint }`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"nautilus.yaml": "name: lift-station\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n" +
					"  - { name: Level, role: input, type: REAL, init: 42.0 }\n" +
					"  - { name: PumpRun, role: output, type: BOOL, init: false }\n" +
					"  - " + bad + "\n",
				"main.st": "PROGRAM Main\nPumpRun := Level > 50.0;\nEND_PROGRAM\n",
			}
			for n, body := range files {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, code := capture(t, func() int { return runCheck([]string{dir}) })
			if code == 0 {
				t.Fatalf("naut check passed a manifest with %s:\n%s", name, out)
			}
			if strings.Contains(out, "undeclared identifier") {
				t.Errorf("naut check blamed the program's tags, not the manifest:\n%s", out)
			}
			if !strings.Contains(out, "nautilus.yaml") || !strings.Contains(out, "tag X") {
				t.Errorf("naut check does not name the manifest's tag X:\n%s", out)
			}
			if n := strings.Count(out, "tag X"); n != 1 {
				t.Errorf("the manifest error is reported %d times, want once:\n%s", n, out)
			}
		})
	}
}
