package runtime_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

// Before the first scan (and on a standby that never scans) the scan
// history is empty: [] in JSON, never null, which the kit's
// ScanDiagnostics read as an array and threw on.
func TestStatsBeforeTheFirstScanAreEmptyNotNull(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM P\nVAR_EXTERNAL X : BOOL; END_VAR\nX := TRUE;\nEND_PROGRAM\n",
		Tags:    []runtime.TagDef{runtime.State("X", false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rt.Stats())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"recent":[]`, `"periods":[]`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("stats before a scan lack %s: %s", k, b)
		}
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("stats before a scan have a null: %s", b)
	}
}
