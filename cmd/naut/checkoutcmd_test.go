package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
)

const ckManifest = `name: plant
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: XV101_Cmd, role: output, type: BOOL, alias: "Local:1:O.Data.0" }
  - { name: LSH101, role: input, type: BOOL, init: false }
  - { name: FIT201, role: input, type: REAL, init: 0.0 }
  - { name: SP_Speed, role: output, init: 0.0 }
  - { name: Pump1, role: input, type: Motor }
  - { name: Mode, role: setpoint, init: 1 }
alarms:
  defs:
    - { id: HiFlow, tag: FIT201, equipment: FT-201 }
auth:
  areas:
    rcv: { tags: ["XV101*", "LSH101"] }
    lod: { tags: ["FIT*"] }
  users:
    alice: [engineer]
`

func ckProject(t *testing.T, manifest string, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"nautilus.yaml": manifest,
		"main.st":       "TYPE Motor : STRUCT Speed : REAL; END_STRUCT END_TYPE\nPROGRAM Main\nVAR_EXTERNAL XV101_Cmd : BOOL; LSH101 : BOOL; FIT201 : REAL; SP_Speed : REAL; Pump1 : Motor; Mode : INT; END_VAR\nXV101_Cmd := LSH101;\nSP_Speed := FIT201;\nEND_PROGRAM\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheckoutInitWritesTheSheetFromTheProject(t *testing.T) {
	dir := ckProject(t, ckManifest, nil)
	var out, errb bytes.Buffer
	if code := checkoutMain([]string{"init", "--project", dir}, &out, &errb); code != 0 {
		t.Fatalf("init: %d\n%s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "wrote checkout.yaml: 4 points") || !strings.Contains(out.String(), "checkout: {}") {
		t.Errorf("stdout: %s", out.String())
	}
	if !strings.Contains(errb.String(), "Pump1") {
		t.Errorf("the struct tag was not reported as skipped: %s", errb.String())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "checkout.yaml"))
	got := string(b)
	for _, want := range []string{
		"id: XV101_Cmd", "kind: DO", "method: force", "address: Local:1:O.Data.0", "area: rcv",
		"id: LSH101", "kind: DI", "id: FIT201", "kind: AI", "equipment: FT-201", "area: lod",
		"id: SP_Speed", "kind: AO",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the sheet lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Mode") || strings.Contains(got, "Pump1") {
		t.Errorf("a setpoint or a struct is on the sheet:\n%s", got)
	}
	// it does not overwrite the engineer's edits
	if code := checkoutMain([]string{"init", "--project", dir}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "--force") {
		t.Errorf("a second init: %d %s", code, errb.String())
	}
}

// The checkout is opt-in: with no checkout: section a project loads as it
// always did (no sheet, nothing to wire), and with one the sheet loads and
// is checked against the project's tags.
func TestCheckoutIsOptIn(t *testing.T) {
	dir := ckProject(t, ckManifest, nil)
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil || m.Checkout != nil {
		t.Fatalf("a project with no checkout: section: %v, %+v", err, m.Checkout)
	}
	// a checkout: section with no sheet is refused with the fix
	dir2 := ckProject(t, ckManifest+"checkout: {}\n", nil)
	if _, err := project.ReadManifest(os.DirFS(dir2), ""); err == nil || !strings.Contains(err.Error(), "naut checkout init") {
		t.Errorf("no sheet: %v", err)
	}
	// with the sheet it loads
	var out, errb bytes.Buffer
	if code := checkoutMain([]string{"init", "--project", dir2}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	m2, err := project.ReadManifest(os.DirFS(dir2), "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(os.DirFS(dir2), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.CheckoutSheet() == nil || len(p.CheckoutSheet().Points) != 4 || m2.Checkout.SignoffRule() != project.SignoffTwoPerson {
		t.Errorf("sheet %+v", p.CheckoutSheet())
	}
	if _, err := project.ReadManifest(os.DirFS(ckProject(t, ckManifest+"checkout: {signoff: maybe}\n", nil)), ""); err == nil || !strings.Contains(err.Error(), "two-person, single") {
		t.Errorf("a bad signoff: %v", err)
	}
}

// naut check holds a checkout sheet to the project: a point names a tag it
// has, a forced point is a declared output, a single-person sign-off warns.
func TestCheckHoldsTheCheckoutSheetToTheProject(t *testing.T) {
	sheet := func(points string) map[string]string {
		return map[string]string{"checkout.yaml": "title: T\npoints:\n" + points}
	}
	good := "  - {id: XV101, tag: XV101_Cmd, kind: DO, method: force}\n  - {id: LSH, tag: LSH101, kind: DI, method: observe}\n"
	for _, c := range []struct {
		name, manifest, points string
		code                   int
		want                   string
	}{
		{"a good sheet", ckManifest + "checkout: {}\n", good, 0, "2 checkout points"},
		{"a tag the project lacks", ckManifest + "checkout: {}\n", good + "  - {id: X, tag: Nope, kind: DI, method: observe}\n", 1, `the project has no tag "Nope"`},
		{"a force on an input", ckManifest + "checkout: {}\n", "  - {id: F, tag: FIT201, kind: AO, method: force}\n", 1, "is not an output the controller drives"},
		{"a single-person sign-off", ckManifest + "checkout: {signoff: single}\n", good, 0, "warning: checkout.signoff: single"},
		{"a sheet with a bad row", ckManifest + "checkout: {}\n", "  - {id: A, tag: LSH101, kind: ZZ, method: observe}\n", 1, `kind "ZZ"`},
	} {
		dir := ckProject(t, c.manifest, sheet(c.points))
		out, code := capture(t, func() int { return runCheck([]string{dir}) })
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%s: naut check = %d\n%s\nwant %d and %q", c.name, code, out, c.code, c.want)
		}
	}
	// no checkout: section, no checkout lines at all
	dir := ckProject(t, ckManifest, nil)
	if out, code := capture(t, func() int { return runCheck([]string{dir}) }); code != 0 || strings.Contains(out, "checkout") {
		t.Errorf("a project without a checkout: naut check = %d\n%s", code, out)
	}
}
