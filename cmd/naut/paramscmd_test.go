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
		"no params":   {"version: 1\n", "no parameters"},
		"empty map":   {"version: 1\nparameters: {}\n", "no parameters"},
		"dup":         {"version: 1\nparameters: {a: 1, a: 2}\n", "appears twice"},
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

func TestDiffSetCaseInsensitiveAndNullNames(t *testing.T) {
	lines, d := diffSet(testParams(), map[string]any{"tank.sp": 50})
	if !d || len(lines) != 1 || lines[0] != "change   Tank.SP: 42.5 -> 50" {
		t.Fatalf("case: %v", lines)
	}
	set, err := decodeParamSet([]byte("version: 1\nparameters: {null: 1, \"~\": 2}\n"))
	if err != nil || set["null"] != 1 || set["~"] != 2 {
		t.Fatalf("null names: %v %v", set, err)
	}
	if lines, _ := diffSet(testParams(), set); len(lines) != 2 {
		t.Fatalf("null names not reported: %v", lines)
	}
}

func TestParamsImportCommand(t *testing.T) {
	var got map[string]any
	status := http.StatusOK
	reply := `{"dryRun":false,"applied":true,"results":[{"name":"Tank.SP","from":42.5,"to":50,"status":"change"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/parameters/import" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	defer srv.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "set.yaml")
	os.WriteFile(file, []byte("version: 1\nparameters: {Tank.SP: 50}\n"), 0o644)

	if c := runParams([]string{"import", "--host", srv.URL, "--file", file, "--dry-run"}); c != 0 {
		t.Fatalf("dry run = %d", c)
	}
	if got["dryRun"] != true || got["version"] != 1.0 || got["parameters"].(map[string]any)["Tank.SP"] != 50.0 {
		t.Fatalf("sent %v", got)
	}
	if c := runParams([]string{"import", "--host", srv.URL, "--file", file}); c != 0 || got["dryRun"] != false {
		t.Fatalf("apply = %d %v", c, got)
	}
	status, reply = http.StatusUnprocessableEntity, `{"results":[{"name":"Tank.SP","status":"refused","reason":"above max"}]}`
	if c := runParams([]string{"import", "--host", srv.URL, "--file", file}); c != 1 {
		t.Fatalf("refused = %d", c)
	}
	status, reply = http.StatusConflict, `{"error":"the set changed","answer":{"results":[{"name":"Tank.SP","status":"applied"}]}}`
	if c := runParams([]string{"import", "--host", srv.URL, "--file", file}); c != 1 {
		t.Fatalf("409 = %d", c)
	}
	status, reply = http.StatusUnauthorized, `{"error":"sign in"}`
	if c := runParams([]string{"import", "--host", srv.URL, "--file", file}); c != 2 {
		t.Fatalf("401 = %d", c)
	}
	if c := runParams([]string{"import", "--host", srv.URL}); c != 2 {
		t.Fatalf("no file = %d", c)
	}
}
