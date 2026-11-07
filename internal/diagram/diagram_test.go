package diagram

import (
	"encoding/json"
	"strings"
	"testing"
)

const sealIn = `PROGRAM Sealin
LD
  RUNG sealin
    [ StartPB | Motor ] /StopPB ( Motor )
END_LD
END_PROGRAM
`

func TestGraphEachLanguage(t *testing.T) {
	m, err := Graph("ld", sealIn, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"rungs"`) || strings.Contains(string(b), `"tags"`) {
		t.Errorf("ld graph without a file: %s", b)
	}
	if _, err := Graph("fbd", "PROGRAM P\nEND_PROGRAM\n", ""); err == nil {
		t.Error("fbd graph of a source with no FBD body: want the CLI's error")
	}
	if _, err := Graph("cobol", "", ""); err == nil {
		t.Error("unknown language: want an error")
	}
}

func TestEditCodesMatchTheCLI(t *testing.T) {
	req, _ := json.Marshal(map[string]any{"source": sealIn, "op": map[string]any{"type": "addRung", "name": "two"}})
	out, code := Edit("ld", req)
	b, _ := json.Marshal(out)
	if code != 0 || !strings.Contains(string(b), "RUNG two") {
		t.Errorf("addRung: code %d, %s", code, b)
	}
	if _, code := Edit("ld", []byte("garbage")); code != 2 {
		t.Errorf("undecodable request: code %d, want 2", code)
	}
	req, _ = json.Marshal(map[string]any{"source": sealIn, "op": map[string]any{"type": "nope"}})
	if _, code := Edit("ld", req); code != 1 {
		t.Errorf("unknown op: code %d, want 1", code)
	}
	// sfc and fbd answer an empty edit list as [], not null.
	req, _ = json.Marshal(map[string]any{"source": "x"})
	if out, _ := Edit("sfc", req); out == nil {
		t.Error("sfc edit: nil answer")
	}
}
