package facade

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// The facade does not edit a CPU's program, and says so; each attempt is
// still an action somebody tried, so it is journalled, refused (#709).
func TestProgramAttemptsAreJournalled(t *testing.T) {
	_, _, base := start(t)
	for _, p := range []struct{ method, path, action string }{
		{"PUT", "/api/program", "program.edit"},
		{"POST", "/api/program/rollback", "program.rollback"},
		{"POST", "/api/program/activate", "program.activate"},
	} {
		req, _ := http.NewRequest(p.method, base+p.path, bytes.NewReader([]byte("{}")))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("%s = %d; want the facade's 403", p.path, res.StatusCode)
		}
		r, err := http.Get(base + "/api/audit?action=" + p.action)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Entries []map[string]any `json:"entries"`
		}
		_ = json.NewDecoder(r.Body).Decode(&out)
		r.Body.Close()
		if len(out.Entries) != 1 || out.Entries[0]["result"] != "refused" {
			t.Fatalf("%s: %v; want one refused entry", p.action, out.Entries)
		}
	}
}
