package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

func f64(v float64) *float64 { return &v }

func testParams() []runtime.Parameter {
	return []runtime.Parameter{
		{Name: "Tank.SP", Value: 42.5, Init: 40.0, Unit: "degC", State: runtime.ParamPlantAhead, Min: f64(0), Max: f64(100), EditedBy: "ann"},
		{Name: "Pump.Run", Value: true, Init: true, State: runtime.ParamInStep},
		{Name: "Line.Name", Value: "A", Init: "A", State: runtime.ParamInStep},
		{Name: "Fan.Speed", Value: 10.0, Init: 10.0, State: runtime.ParamInStep, Max: f64(50)},
	}
}

func paramServer(t *testing.T) (host string, port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/parameters" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"writeBack": "off", "parameters": testParams()})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, 0
}

func TestParamSetRoundTrip(t *testing.T) {
	b, err := encodeParamSet(testParams())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"version: 1", "Tank.SP: 42.5 # degC; plant-ahead, init 40", "Pump.Run: true", "Line.Name: A"} {
		if !strings.Contains(s, want) {
			t.Fatalf("export lacks %q:\n%s", want, s)
		}
	}
	// Names are sorted.
	if strings.Index(s, "Fan.Speed") > strings.Index(s, "Tank.SP") {
		t.Fatalf("not sorted:\n%s", s)
	}
	set, err := decodeParamSet(b)
	if err != nil {
		t.Fatal(err)
	}
	if set["Tank.SP"] != 42.5 || set["Pump.Run"] != true || set["Line.Name"] != "A" || set["Fan.Speed"] != 10 {
		t.Fatalf("round trip: %#v", set)
	}
	// Exporting what you diff against yourself is a no change.
	if lines, d := diffSet(testParams(), set); d {
		t.Fatalf("a self export differs: %v", lines)
	}
}

func TestParamSetRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"empty":       {"", "empty file"},
		"no version":  {"parameters: {a: 1}\n", "version 0 is not supported"},
		"future":      {"version: 2\nparameters: {}\n", "version 2 is not supported"},
		"unknown key": {"version: 1\nparametres: {}\n", "parametres"},
		"nested":      {"version: 1\nparameters: {a: [1]}\n", `parameter "a"`},
	} {
		if _, err := decodeParamSet([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestDiffSet(t *testing.T) {
	set := map[string]any{
		"Tank.SP":   101,  // above max
		"Pump.Run":  1,    // kind
		"Line.Name": "B",  // change
		"Fan.Speed": 60.0, // above max
		"Nope":      1,    // unknown
		"Pump.x":    true, // unknown too
	}
	lines, d := diffSet(testParams(), set)
	if !d {
		t.Fatal("no difference reported")
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"REFUSED  Tank.SP: 101 is above max 100",
		"REFUSED  Pump.Run: true is a bool, the file has a number (1)",
		`change   Line.Name: "A" -> "B"`,
		"REFUSED  Fan.Speed: 60 is above max 50",
		"REFUSED  Nope: not a parameter",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// A value below min, and an in-range change.
	lines, _ = diffSet(testParams(), map[string]any{"Tank.SP": -1.0})
	if !strings.Contains(lines[0], "below min 0") {
		t.Errorf("below min: %v", lines)
	}
	lines, _ = diffSet(testParams(), map[string]any{"Tank.SP": 50})
	if len(lines) != 1 || lines[0] != "change   Tank.SP: 42.5 -> 50" {
		t.Errorf("in range: %v", lines)
	}
}

func TestDiffAgainstRepo(t *testing.T) {
	lines, d := diffAgainstRepo(testParams())
	if !d || len(lines) != 1 || !strings.Contains(lines[0], "plant-ahead") || !strings.Contains(lines[0], "live 42.5, repo 40") || !strings.Contains(lines[0], "edited by ann") {
		t.Fatalf("lines = %v", lines)
	}
	if _, d := diffAgainstRepo(paramsDrifted(nil)); d {
		t.Fatal("empty differs")
	}
}

func TestParamsCommands(t *testing.T) {
	host, _ := paramServer(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "set.yaml")
	if c := runParams([]string{"export", "--host", host, "-o", out}); c != 0 {
		t.Fatalf("export exit %d", c)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "Tank.SP: 42.5") {
		t.Fatalf("file:\n%s", b)
	}
	// --drift keeps only the drifted one.
	if c := runParams([]string{"export", "--host", host, "--drift", "-o", out}); c != 0 {
		t.Fatal(c)
	}
	b, _ = os.ReadFile(out)
	if strings.Contains(string(b), "Pump.Run") || !strings.Contains(string(b), "Tank.SP") {
		t.Fatalf("drift file:\n%s", b)
	}
	// diff: repo drift under --check exits 1; a matching file exits 0.
	if c := runParams([]string{"diff", "--host", host, "--check"}); c != 1 {
		t.Fatalf("repo diff --check = %d", c)
	}
	if c := runParams([]string{"diff", "--host", host}); c != 0 {
		t.Fatalf("repo diff = %d", c)
	}
	if c := runParams([]string{"diff", "--host", host, "--file", out, "--check"}); c != 0 {
		t.Fatalf("self diff --check = %d", c)
	}
	os.WriteFile(out, []byte("version: 1\nparameters: {Tank.SP: 500}\n"), 0o644)
	if c := runParams([]string{"diff", "--host", host, "--file", out, "--check"}); c != 1 {
		t.Fatalf("refused diff --check = %d", c)
	}
	// Failures are 2.
	if c := runParams([]string{"diff", "--host", host, "--file", filepath.Join(dir, "none")}); c != 2 {
		t.Fatalf("missing file = %d", c)
	}
	if c := runParams([]string{"export"}); c != 2 {
		t.Fatalf("no host = %d", c)
	}
	if c := runParams([]string{"bogus"}); c != 2 {
		t.Fatalf("bogus = %d", c)
	}
	if c := runParams([]string{"export", "--host", "127.0.0.1", "--port", "1"}); c != 2 {
		t.Fatalf("unreachable = %d", c)
	}
}
