package mimicdoc

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The same fixture the kit's expandUnits is tested against
// (hmi/tests/mimic.test.ts): naut and the screen draw one answer.
func TestExpandUnitsMatchesTheKit(t *testing.T) {
	in, err := os.ReadFile("../../testdata/mimic-units/in.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../testdata/mimic-units/out.json")
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := ExpandUnits(in)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		gb, _ := json.MarshalIndent(g, "", " ")
		t.Errorf("naut's expansion differs from the kit's:\n%s", gb)
	}
}

func TestNoUnitsIsUnchanged(t *testing.T) {
	in := []byte(`{"canvas":{"width":1,"height":1},"equipment":[{"id":"a","x":1.5}]}`)
	out, changed, err := ExpandUnits(in)
	if err != nil || changed || string(out) != string(in) {
		t.Errorf("changed=%v err=%v out=%s", changed, err, out)
	}
	if out, changed, _ := ExpandUnits([]byte("not json")); changed || string(out) != "not json" {
		t.Errorf("not json: %s", out)
	}
}
