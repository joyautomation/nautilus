package rationalize

import (
	"bytes"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/alarm"
)

func masterDefs() []alarm.Def {
	return []alarm.Def{
		{
			ID: "PIT101_HH", Name: "Inlet pressure high high", Tag: "PIT101.HH", Priority: alarm.Critical,
			Class: "process", Site: "plant", Area: "inlet",
			Consequence: `Line over-pressure, "relief" lifts`,
			Response:    "Stop P101, then\nclose XV101",
			Rationalization: alarm.Rationalization{
				State: alarm.RationalizationApproved, By: "alice", At: "2026-11-20",
			},
			Source: "manifest",
		},
		{ID: "LIT101_L", Name: "Wet well low", Tag: "LIT101.L", Priority: alarm.High, Source: "manifest rule 0 (match type=AnalogInput member=L)"},
		{ID: "AAA_DOOR", Name: "Door open", Tag: "Door", Priority: alarm.Low, Response: "Close it", Source: "alarms/doors.yaml"},
	}
}

// Export then check reports nothing, through CSV and through YAML, with
// text holding a comma, a quote and a newline; and the export is sorted by
// id, so two runs write the same bytes.
func TestMasterRoundTrip(t *testing.T) {
	rows := Rows(masterDefs())
	for i, want := range []string{"AAA_DOOR", "LIT101_L", "PIT101_HH"} {
		if rows[i].ID != want {
			t.Fatalf("row %d = %s, want %s (sorted by id)", i, rows[i].ID, want)
		}
	}
	if rows[0].State != "pending" {
		t.Errorf("an alarm with no block exports state %q, want pending", rows[0].State)
	}

	var c bytes.Buffer
	if err := WriteCSV(&c, rows); err != nil {
		t.Fatal(err)
	}
	back, err := ReadCSV(strings.NewReader(c.String()))
	if err != nil {
		t.Fatal(err)
	}
	if d := Compare(back, rows); len(d) != 0 {
		t.Errorf("CSV round trip differs: %v\n%s", d, c.String())
	}

	var y bytes.Buffer
	if err := WriteYAML(&y, rows); err != nil {
		t.Fatal(err)
	}
	back, err = ReadYAML(strings.NewReader(y.String()))
	if err != nil {
		t.Fatal(err)
	}
	if d := Compare(back, rows); len(d) != 0 {
		t.Errorf("YAML round trip differs: %v\n%s", d, y.String())
	}
}

// A spreadsheet reorders columns, adds a BOM and CRLF line endings, pads
// cells and leaves blank rows; none of that is a difference. Priority and
// state compare without case.
func TestMasterReadsASpreadsheetsCSV(t *testing.T) {
	csv := "\ufeffID,Priority,Name,Tag,State,By,At,Response\r\n" +
		"AAA_DOOR,LOW,Door open,Door,,,,Close it \r\n" +
		",,,,,,,\r\n"
	rows, err := ReadCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	manifest := Rows(masterDefs()[2:])
	if d := Compare(rows, manifest); len(d) != 0 {
		t.Errorf("differs: %v", d)
	}
}

func TestMasterRefusesAnUnreadableHeader(t *testing.T) {
	for name, csv := range map[string]string{
		"unknown column": "id,priorty\nA,low\n",
		"no id column":   "name,priority\nA,low\n",
		"twice":          "id,id\nA,A\n",
		"empty":          "",
		"row with no id": "id,name\n,Door\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadCSV(strings.NewReader(csv)); err == nil {
				t.Fatal("read it")
			}
		})
	}
	for _, empty := range []string{"", "null\n", "[]\n"} {
		if _, err := ReadYAML(strings.NewReader(empty)); err == nil && empty != "[]\n" {
			t.Errorf("YAML master %q read as no alarms", empty)
		}
	}
	if _, err := ReadYAML(strings.NewReader("- id: A\n  priorty: low\n")); err == nil {
		t.Error("YAML master took an unknown key")
	}
}

// Every way the master and the manifest can disagree is named, by id,
// with where the manifest declares the alarm.
func TestMasterCompare(t *testing.T) {
	manifest := Rows(masterDefs())
	master := Rows(masterDefs())
	master[2].Priority = "high"      // PIT101_HH
	master[2].Response = "Stop P101" // and its response
	master[1].State = "reviewed"     // LIT101_L
	master = append(master[1:], Row{ID: "OLD_ALARM", Priority: "low"}, master[1])
	// master now: LIT101_L, PIT101_HH, OLD_ALARM, LIT101_L (duplicate); AAA_DOOR missing.

	got := Compare(master, manifest)
	want := []Difference{
		{ID: "AAA_DOOR", Kind: OnlyInManifest, Source: "alarms/doors.yaml"},
		{ID: "LIT101_L", Kind: Duplicate},
		{ID: "LIT101_L", Kind: Differs, Field: "state", Master: "reviewed", Manifest: "pending", Source: manifest[1].Source},
		{ID: "OLD_ALARM", Kind: OnlyInMaster},
		{ID: "PIT101_HH", Kind: Differs, Field: "priority", Master: "high", Manifest: "critical", Source: "manifest"},
		{ID: "PIT101_HH", Kind: Differs, Field: "response", Master: "Stop P101", Manifest: "Stop P101, then\nclose XV101", Source: "manifest"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d differences, want %d:\n%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("difference %d = %+v\nwant %+v", i, got[i], want[i])
		}
	}
	// Source is where to look, not something to agree on.
	m2 := Rows(masterDefs())
	m2[0].Source = "somewhere else"
	if d := Compare(m2, manifest); len(d) != 0 {
		t.Errorf("source compared: %v", d)
	}
}

func TestCoverage(t *testing.T) {
	c := Cover(masterDefs())
	if c.Total != 3 || c.Pending != 2 || c.Approved != 1 || c.Reviewed != 0 {
		t.Errorf("counts = %+v", c)
	}
	if len(c.PendingHigh) != 1 || c.PendingHigh[0] != "LIT101_L" {
		t.Errorf("pending high = %v, want [LIT101_L] (the low door alarm is not high)", c.PendingHigh)
	}
	if c.NoResponse != 1 {
		t.Errorf("no response = %d, want 1 (LIT101_L)", c.NoResponse)
	}
}
