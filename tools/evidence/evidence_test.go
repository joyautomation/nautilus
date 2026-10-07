package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestClaimsCheck holds the real inventory to the source tree, so a test
// rename that orphans a claim fails `go test ./...` on the PR that does it.
func TestClaimsCheck(t *testing.T) {
	files, err := loadClaims("../../docs/claims")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("docs/claims has no claim files")
	}
	for _, e := range checkClaims("../..", files) {
		t.Error(e)
	}
}

func TestParseRef(t *testing.T) {
	r, err := parseRef("go ./lang/conformance TestConformance/fb-ton/Q false at PT-1ms, true at PT")
	if err != nil {
		t.Fatal(err)
	}
	if r.Dir != "./lang/conformance" || r.Test != "TestConformance/fb-ton/Q false at PT-1ms, true at PT" {
		t.Errorf("parsed %+v", r)
	}
	if got := r.GoName(); got != "TestConformance/fb-ton/Q_false_at_PT-1ms,_true_at_PT" {
		t.Errorf("GoName = %q", got)
	}
	n, err := parseRef("naut examples/lift-station duty alternates every completed cycle")
	if err != nil || n.Kind != "naut" || n.Test != "duty alternates every completed cycle" {
		t.Errorf("naut ref: %+v %v", n, err)
	}
	for _, bad := range []string{"TestX", "go modbus TestX", "go ./modbus", "py ./x TestY"} {
		if _, err := parseRef(bad); err == nil {
			t.Errorf("%q parsed; want an error", bad)
		}
	}
}

const goJSON = `{"Action":"run","Package":"github.com/joyautomation/nautilus/modbus","Test":"TestA"}
{"Action":"pass","Package":"github.com/joyautomation/nautilus/modbus","Test":"TestA/one_sub"}
{"Action":"fail","Package":"github.com/joyautomation/nautilus/modbus","Test":"TestA/two"}
{"Action":"fail","Package":"github.com/joyautomation/nautilus/modbus","Test":"TestA"}
{"Action":"skip","Package":"github.com/joyautomation/nautilus/modbus","Test":"TestForeign"}
{"Action":"skip","Package":"github.com/joyautomation/nautilus/modbus","Test":"TestB"}
{"Action":"fail","Package":"github.com/joyautomation/nautilus/modbus"}
`

// The foreign-stack job runs what the plain job skips.
const foreignJSON = `{"Action":"pass","Package":"github.com/joyautomation/nautilus/modbus","Test":"TestForeign/read_coils"}
{"Action":"pass","Package":"github.com/joyautomation/nautilus/modbus","Test":"TestForeign"}
`

func TestLookup(t *testing.T) {
	r := newResults()
	if err := r.readGoJSON(strings.NewReader(goJSON)); err != nil {
		t.Fatal(err)
	}
	if err := r.readGoJSON(strings.NewReader(foreignJSON)); err != nil {
		t.Fatal(err)
	}
	if err := r.readNautJSON(strings.NewReader(`{"root":"examples/lift-station","name":"duty alternates","passed":true}` + "\n")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ref, status string
		passed      int
	}{
		{"go ./modbus TestA/one sub", "pass", 1},
		{"go ./modbus TestA", "fail", 1},           // itself failed + one sub passed + one failed
		{"go ./modbus TestForeign", "pass", 2},     // pass merged over skip, plus its subtest
		{"go ./modbus TestB/never ran", "skip", 0}, // parent skipped: hardware-gated
		{"go ./modbus TestC", "missing", 0},        // nobody reported it
		{"go ./other TestA", "missing", 0},         // wrong package
		{"naut examples/lift-station duty alternates", "pass", 1},
	}
	for _, c := range cases {
		ref, err := parseRef(c.ref)
		if err != nil {
			t.Fatal(err)
		}
		got := r.lookup(ref)
		if got.Status != c.status || got.Passed != c.passed {
			t.Errorf("%s: got %s (passed %d), want %s (passed %d)", c.ref, got.Status, got.Passed, c.status, c.passed)
		}
	}
	if r.Pkg["./modbus"] != Fail {
		t.Errorf("package status = %q, want fail", r.Pkg["./modbus"])
	}
}

func TestVerdict(t *testing.T) {
	ref := func(s ...string) []RefResult {
		var out []RefResult
		for _, st := range s {
			out = append(out, RefResult{Status: st})
		}
		return out
	}
	cases := []struct {
		c    ClaimResult
		want string
	}{
		{ClaimResult{}, "gap"},
		{ClaimResult{Refs: ref("pass", "skip")}, "verified"},
		{ClaimResult{Refs: ref("pass"), Partial: true}, "partial"},
		{ClaimResult{Refs: ref("pass", "fail")}, "failing"},
		{ClaimResult{Refs: ref("skip", "missing")}, "unrun"},
	}
	for _, c := range cases {
		if got := verdict(c.c); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.c.Refs, got, c.want)
		}
	}
}

func TestReadCover(t *testing.T) {
	prof := `mode: set
github.com/joyautomation/nautilus/modbus/a.go:1.1,2.2 3 1
github.com/joyautomation/nautilus/modbus/a.go:3.1,4.2 2 0
github.com/joyautomation/nautilus/modbus/a.go:3.1,4.2 2 1
github.com/joyautomation/nautilus/lang/st/b.go:1.1,2.2 5 0
`
	got, err := readCover(strings.NewReader(prof))
	if err != nil {
		t.Fatal(err)
	}
	if got["./modbus"] != [2]int{5, 5} {
		t.Errorf("modbus = %v, want [5 5] (the repeated block counts once, covered)", got["./modbus"])
	}
	if got["./lang/st"] != [2]int{5, 0} {
		t.Errorf("lang/st = %v, want [5 0]", got["./lang/st"])
	}
}

func TestCat(t *testing.T) {
	var out bytes.Buffer
	err := runCat(strings.NewReader(`{"Action":"output","Output":"=== RUN TestA\n"}
{"Action":"output","Output":"ok  \tpkg\t0.1s\n"}
{"Action":"pass","Package":"pkg"}
`), &out)
	if err != nil || out.String() != "=== RUN TestA\nok  \tpkg\t0.1s\n" {
		t.Errorf("cat: %q, %v", out.String(), err)
	}
	if err := runCat(strings.NewReader(`{"Action":"fail","Package":"pkg"}`), &out); err == nil {
		t.Error("cat of a failing stream returned nil")
	}
}
