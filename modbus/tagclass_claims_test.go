package modbus

import (
	"reflect"
	"testing"
	"time"
)

// MB-012: WithTagClass (what tag-classes: in nautilus.yaml becomes) assigns
// tags to a scan class over the class the generated manifest wrote, and a
// later assignment beats an earlier one, so re-running the import (which
// rewrites the manifest) never erases the policy.
func TestTagClassOverridesTheManifestScanClass(t *testing.T) {
	srv := newSlave(t)
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "T_Raw", Source: "DEV", Table: TableInput, Address: 5, Format: "uint16", ScanClass: "slow"},
			{Name: "T_Temp", Source: "DEV", Table: TableHolding, Address: 0, Format: "int16"},
			{Name: "Other", Source: "DEV", Table: TableHolding, Address: 1, Format: "int16"},
		},
	}
	classes := func(opts ...Option) map[string][]string {
		t.Helper()
		base := []Option{WithScanRate(20 * time.Millisecond),
			WithScanClass("slow", 60*time.Millisecond), WithScanClass("fast", 10*time.Millisecond)}
		d, err := New(m, append(base, opts...)...)
		if err != nil {
			t.Fatal(err)
		}
		return d.ScanClasses()
	}

	// Without an assignment the manifest's own class stands.
	if got := classes()["slow"]; !reflect.DeepEqual(got, []string{"T_Raw"}) {
		t.Fatalf("manifest class: slow = %v, want [T_Raw]", got)
	}
	// A glob beats it, and reaches the tags the manifest left in the default class.
	got := classes(WithTagClass("fast", "T_*"))
	if !reflect.DeepEqual(got["fast"], []string{"T_Raw", "T_Temp"}) || len(got["slow"]) != 0 {
		t.Errorf("after WithTagClass(fast, T_*): fast = %v, slow = %v; want fast [T_Raw T_Temp], slow empty", got["fast"], got["slow"])
	}
	if !reflect.DeepEqual(got[DefaultClass], []string{"Other"}) {
		t.Errorf("default = %v, want [Other]", got[DefaultClass])
	}
	// The later assignment wins.
	got = classes(WithTagClass("fast", "T_*"), WithTagClass("slow", "T_Temp"))
	if !reflect.DeepEqual(got["slow"], []string{"T_Temp"}) || !reflect.DeepEqual(got["fast"], []string{"T_Raw"}) {
		t.Errorf("later assignment: fast = %v, slow = %v; want fast [T_Raw], slow [T_Temp]", got["fast"], got["slow"])
	}
}
