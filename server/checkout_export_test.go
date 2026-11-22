//go:build !js

package server

import (
	csvpkg "encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/checkout"
)

// The export is a read of a run: every result kept (a fail before a pass), the
// file says in its content what it is, a spreadsheet formula in a note is
// defused, and the file's name comes from the run's id alone.
func TestCheckoutExport(t *testing.T) {
	srv, _ := ckServer(t)
	eng := ckAs(t, srv, "akhan")
	rec := call(t, srv, "POST", "/api/checkout/runs", `{}`, eng)
	var run checkout.Run
	json.Unmarshal(rec.Body.Bytes(), &run)
	path := "/api/checkout/runs/" + run.ID
	for _, b := range []string{
		`{"point":"SP","outcome":"fail","note":"stuck high"}`,
		`{"point":"SP","outcome":"pass"}`,
		`{"point":"LVL","outcome":"note","note":"=HYPERLINK(\"http://x\",\"y\")"}`,
	} {
		if rec := call(t, srv, "POST", path+"/results", b, eng); rec.Code != http.StatusCreated {
			t.Fatalf("result %s: %d %s", b, rec.Code, rec.Body)
		}
	}

	// JSON: both SP results, oldest first, the latest as its status; OUT not checked
	rec = call(t, srv, "GET", path+"/export", "", eng)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="checkout-`+run.ID+`.json"` {
		t.Errorf("Content-Disposition %q", cd)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", rec.Header())
	}
	var ex checkout.Export
	if err := json.Unmarshal(rec.Body.Bytes(), &ex); err != nil {
		t.Fatal(err)
	}
	if ex.Format != checkout.ExportFormat || ex.Sealed || ex.Rehearsal || !strings.Contains(ex.Status, "UNSEALED") {
		t.Errorf("export header %+v", ex)
	}
	byID := map[string]checkout.ExportPoint{}
	for _, p := range ex.Points {
		byID[p.ID] = p
	}
	if sp := byID["SP"]; len(sp.Results) != 2 || sp.Results[0].Outcome != "fail" || sp.Results[1].Outcome != "pass" || sp.Status != "pass" {
		t.Errorf("SP: %+v, want the fail then the pass, status pass", sp)
	}
	if out := byID["OUT"]; out.Status != checkout.NotChecked || len(out.Results) != 0 {
		t.Errorf("OUT: %+v", out)
	}
	if len(ex.Points) != 3 || ex.Points[0].ID != "SP" || ex.Points[2].ID != "OUT" {
		t.Errorf("points not in the sheet's order: %+v", ex.Points)
	}
	// the same run exports to the same bytes
	if again := call(t, srv, "GET", path+"/export", "", eng); again.Body.String() != rec.Body.String() {
		t.Errorf("two exports of one run differ")
	}

	// CSV: parsed, one row per result and one for the point with none; the
	// formula is defused; every row says what the record is
	rec = call(t, srv, "GET", path+"/export?format=csv", "", eng)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.HasSuffix(rec.Header().Get("Content-Disposition"), `.csv"`) {
		t.Fatalf("csv: %d %v", rec.Code, rec.Header())
	}
	rows, err := csvpkg.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1+2+1+1 { // header, SP x2, LVL x1, OUT not checked
		t.Fatalf("%d rows: %v", len(rows), rows)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	for _, r := range rows[1:] {
		if !strings.Contains(r[col["record"]], "UNSEALED") {
			t.Errorf("a row that does not say it is unsealed: %v", r)
		}
		for _, c := range r {
			if c != "" && strings.ContainsAny(c[:1], "=+@\t\r") {
				t.Errorf("a cell that starts like a formula: %q", c)
			}
		}
	}
	var sawNote bool
	for _, r := range rows[1:] {
		if r[col["point"]] == "LVL" {
			sawNote = true
			if r[col["note"]] != `'=HYPERLINK("http://x","y")` {
				t.Errorf("note cell %q, want the formula defused with a leading apostrophe", r[col["note"]])
			}
		}
	}
	if !sawNote {
		t.Errorf("no LVL row: %v", rows)
	}

	// errors
	if rec := call(t, srv, "GET", path+"/export?format=xml", "", eng); rec.Code != http.StatusBadRequest {
		t.Errorf("format xml: %d", rec.Code)
	}
	if rec := call(t, srv, "GET", "/api/checkout/runs/none/export", "", eng); rec.Code != http.StatusNotFound {
		t.Errorf("no such run: %d", rec.Code)
	}
	// a read like the run's own: no credential is refused as it is, one who may read is not
	if rec := call(t, srv, "GET", path+"/export", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credential: %d, want 401", rec.Code)
	}
	if rec := call(t, srv, "GET", path+"/export", "", ckAs(t, srv, "jsmith")); rec.Code != http.StatusOK {
		t.Errorf("an operator: %d, want 200 (the run's own GET is open to them)", rec.Code)
	}
}

// A rehearsal says so in the content and the filename stays the id's.
func TestCheckoutExportOfARehearsalSaysSo(t *testing.T) {
	srv, _ := ckServer(t)
	eng := ckAs(t, srv, "akhan")
	var run checkout.Run
	json.Unmarshal(call(t, srv, "POST", "/api/checkout/runs", `{"rehearsal":true}`, eng).Body.Bytes(), &run)
	rec := call(t, srv, "GET", "/api/checkout/runs/"+run.ID+"/export", "", eng)
	var ex checkout.Export
	json.Unmarshal(rec.Body.Bytes(), &ex)
	if !ex.Rehearsal || !strings.Contains(ex.Status, "REHEARSAL") || !strings.Contains(ex.Status, "never be signed") {
		t.Errorf("a rehearsal's export: %+v", ex)
	}
	csvOut := call(t, srv, "GET", "/api/checkout/runs/"+run.ID+"/export?format=csv", "", eng).Body.String()
	if !strings.Contains(csvOut, "REHEARSAL") {
		t.Errorf("a rehearsal's csv does not say so:\n%s", csvOut)
	}
}
