package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/devprofile"
)

const lintProfile = `profile: lintme
title: Lint me
protocol: modbus
docs:
  - { id: m, title: manual, url: "https://example.invalid/m.pdf" }
points:
  - { name: Cmd, type: UINT, table: holding, address: %d, writable: true, doc: "m p. 1" }
  - { name: Stat, type: UINT, table: holding, address: 1, doc: "m p. 1" }
simulator: { block: %s, source: lintme.st }
`

const lintSim = `FUNCTION_BLOCK LINT_SIM
VAR_INPUT
  Cmd : UINT;
END_VAR
VAR_OUTPUT
  Stat : UINT;
END_VAR
%s
END_FUNCTION_BLOCK
`

func lintProject(t *testing.T, cmdAddr int, block, body string) string {
	t.Helper()
	dir := t.TempDir()
	prof := strings.NewReplacer("%d", string(rune('0'+cmdAddr)), "%s", block).Replace(lintProfile)
	for f, s := range map[string]string{
		"lintme.yaml": prof,
		"lintme.st":   strings.Replace(lintSim, "%s", body, 1),
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "lintme.yaml")
}

func TestDevicesLint(t *testing.T) {
	for _, tc := range []struct {
		name        string
		addr        int
		block, body string
		wantCode    int
		wantOut     string
	}{
		{"clean", 2, "LINT_SIM", "Stat := Cmd;", 0, "lintme: ok"},
		{"two points on one address", 1, "LINT_SIM", "Stat := Cmd;", 1, "holding address 1 is also Cmd"},
		{"simulator block missing", 2, "NOT_THERE", "Stat := Cmd;", 1, "no FUNCTION_BLOCK NOT_THERE"},
		{"simulator does not compile", 2, "LINT_SIM", "Stat := Nope;", 1, "error: simulator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			code := runDevicesLint([]string{lintProject(t, tc.addr, tc.block, tc.body)}, &out)
			if code != tc.wantCode || !strings.Contains(out.String(), tc.wantOut) {
				t.Fatalf("code %d (want %d), output:\n%s\nwant it to contain %q", code, tc.wantCode, out.String(), tc.wantOut)
			}
		})
	}
}

func TestDevicesLintBuiltinLibraryHasNoErrors(t *testing.T) {
	ps, err := devprofile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		var out bytes.Buffer
		if code := runDevicesLint([]string{p.ID}, &out); code != 0 {
			t.Errorf("%s: exit %d\n%s", p.ID, code, out.String())
		}
	}
}
