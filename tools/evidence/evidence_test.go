package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestClaimsCheck holds the real inventory to the source tree. It is
// opt-in (NAUTILUS_CLAIMS_CHECK=1): claims never block a functional PR.
// CI's runtime-evidence job runs the same check, reports it on PRs and
// keeps a rolling issue on main; `go run ./tools/evidence check` runs it
// by hand.
func TestClaimsCheck(t *testing.T) {
	if os.Getenv("NAUTILUS_CLAIMS_CHECK") == "" {
		t.Skip("set NAUTILUS_CLAIMS_CHECK=1 (or run `go run ./tools/evidence check`)")
	}
	files, problems, err := loadClaims("../../docs/claims")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("docs/claims has no claim files")
	}
	for _, e := range append(problems, checkClaims("../..", files)...) {
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

func TestSummarize(t *testing.T) {
	base := &Evidence{Verdicts: map[string]int{"verified": 2, "gap": 1}, Claims: []ClaimResult{
		{ID: "MB-001", Verdict: "verified"}, {ID: "MB-002", Verdict: "verified"}, {ID: "MB-003", Verdict: "gap"},
	}}
	ev := &Evidence{Verdicts: map[string]int{"verified": 1, "unrun": 1, "gap": 1}, Claims: []ClaimResult{
		{ID: "MB-001", Verdict: "verified"}, {ID: "MB-002", Verdict: "unrun"}, {ID: "MB-003", Verdict: "gap"},
	}, Missing: []string{"go ./modbus TestGone"}}
	got := summarize(ev, base)
	for _, want := range []string{"| verified | 1 (-1) |", "| unrun | 1 (+1) |", "| gap | 1 |", "`go ./modbus TestGone`", "`MB-002: verified → unrun`"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if clean := summarize(&Evidence{Verdicts: map[string]int{}}, nil); !strings.Contains(clean, "Every claim checks") {
		t.Errorf("a clean run should say so:\n%s", clean)
	}
}

// The gate job reports gate checks; the last blackbox-release run (nightly
// on main, or a CLI release) reports the rest.
const gateJSON = `{"Action":"pass","Package":"github.com/joyautomation/nautilus/tools/blackbox","Test":"TestBlackbox/001-F01-gate"}
{"Action":"pass","Package":"github.com/joyautomation/nautilus/tools/blackbox","Test":"TestBlackbox"}
`

const releaseJSON = `{"Action":"pass","Package":"github.com/joyautomation/nautilus/tools/blackbox","Test":"TestBlackbox/001-P-rel-pass"}
{"Action":"fail","Package":"github.com/joyautomation/nautilus/tools/blackbox","Test":"TestBlackbox/001-P-rel-fail"}
{"Action":"fail","Package":"github.com/joyautomation/nautilus/tools/blackbox","Test":"TestBlackbox"}
`

func TestResolveBlackbox(t *testing.T) {
	res, rel := newResults(), newResults()
	if err := res.readGoJSON(strings.NewReader(gateJSON)); err != nil {
		t.Fatal(err)
	}
	if err := rel.readGoJSON(strings.NewReader(releaseJSON)); err != nil {
		t.Fatal(err)
	}
	disp := map[string]string{
		"001-F01-gate":     "gate",
		"001-P-rel-pass":   "release",
		"001-P-rel-fail":   "release",
		"001-P-rel-unrun":  "release",
		"001-P-manual-one": "manual",
	}
	cases := []struct {
		name, ref, status, from string
	}{
		{"gate ref, this run", "go ./tools/blackbox TestBlackbox/001-F01-gate", "pass", ""},
		{"release ref with release evidence", "go ./tools/blackbox TestBlackbox/001-P-rel-pass", "pass", "blackbox-release"},
		{"failed release check", "go ./tools/blackbox TestBlackbox/001-P-rel-fail", "fail", "blackbox-release"},
		{"release ref without release evidence", "go ./tools/blackbox TestBlackbox/001-P-rel-unrun", ReleaseOnly, ""},
		{"manual check, never run", "go ./tools/blackbox TestBlackbox/001-P-manual-one", ReleaseOnly, ""},
		{"unknown id stays missing", "go ./tools/blackbox TestBlackbox/001-P-renamed", "missing", ""},
		{"not a black-box ref", "go ./modbus TestA", "missing", ""},
	}
	for _, c := range cases {
		ref, err := parseRef(c.ref)
		if err != nil {
			t.Fatal(err)
		}
		got := resolve(ref, res, rel, disp)
		if got.Status != c.status || got.From != c.from {
			t.Errorf("%s: got %s from %q, want %s from %q", c.name, got.Status, got.From, c.status, c.from)
		}
	}
	// A release check that failed fails its claim; one not yet run leaves
	// the claim unrun, which is not a problem.
	if v := verdict(ClaimResult{Refs: []RefResult{{Status: "pass"}, {Status: "fail", From: "blackbox-release"}}}); v != "failing" {
		t.Errorf("claim with a failed release check: %s, want failing", v)
	}
	if v := verdict(ClaimResult{Refs: []RefResult{{Status: ReleaseOnly}}}); v != "unrun" {
		t.Errorf("claim with only a release-only check: %s, want unrun", v)
	}
}

// A black-box reference must name a check the manifest has: a renamed id
// is the case the "no job reported" warning exists for, so `check` catches
// it before any run.
func TestCheckBlackboxID(t *testing.T) {
	root := t.TempDir()
	dir := root + "/tools/blackbox"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(dir+"/"+name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("blackbox_test.go", "package blackbox\n\nfunc TestBlackbox(t *testing.T) {}\n")
	write("checks.yaml", "checks:\n- id: 001-F01-gate\n  disposition: gate\n- id: 001-P-rel\n  disposition: release\n")
	c := &checker{root: root, funcs: map[string]map[string]bool{}, suite: map[string]map[string]bool{}}
	for _, ok := range []string{"TestBlackbox/001-F01-gate", "TestBlackbox/001-P-rel", "TestBlackbox"} {
		r, _ := parseRef("go ./tools/blackbox " + ok)
		c.checkRef("X-001", r)
	}
	if len(c.errs) != 0 {
		t.Fatalf("known ids flagged: %v", c.errs)
	}
	r, _ := parseRef("go ./tools/blackbox TestBlackbox/001-P-renamed")
	c.checkRef("X-001", r)
	if len(c.errs) != 1 || !strings.Contains(c.errs[0], "no check 001-P-renamed in tools/blackbox/checks.yaml") {
		t.Errorf("unknown id: %v", c.errs)
	}
}

func TestSummarizeRelease(t *testing.T) {
	ev := &Evidence{Verdicts: map[string]int{"failing": 1, "unrun": 1}, Claims: []ClaimResult{
		{ID: "UA-001", Verdict: "failing", Refs: []RefResult{{Ref: "go ./tools/blackbox TestBlackbox/001-P-rel-fail", Status: "fail", From: "blackbox-release"}}},
		{ID: "UA-002", Verdict: "unrun", Refs: []RefResult{{Ref: "go ./tools/blackbox TestBlackbox/001-P-rel-unrun", Status: ReleaseOnly}}},
	}, ReleaseOnly: []string{"go ./tools/blackbox TestBlackbox/001-P-rel-unrun"}}
	got := summarize(ev, nil)
	for _, want := range []string{
		"**Failing in the last blackbox-release run** (1)", "`UA-001: go ./tools/blackbox TestBlackbox/001-P-rel-fail`",
		"(informational)** (1)", "release-only checks aside",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "References no job reported") {
		t.Errorf("a release-only reference is not a missing one:\n%s", got)
	}
}
