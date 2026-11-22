package checkout

import (
	"bytes"
	"strings"
	"testing"
)

func TestNeutralize(t *testing.T) {
	for in, want := range map[string]string{
		"=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@SUM(A1)": "'@SUM(A1)", "\tx": "'\tx", "\rx": "'\rx",
		"  =1+1": "'  =1+1", "plain": "plain", "": "", "  ": "  ", "a=b": "a=b",
	} {
		if got := Neutralize(in); got != want {
			t.Errorf("Neutralize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileStemIsBuiltFromTheIDAlone(t *testing.T) {
	if got := FileStem(`r1/../"x y".csv`); strings.ContainsAny(got, `/\" .`) || !strings.HasPrefix(got, "checkout-") {
		t.Errorf("FileStem = %q", got)
	}
}

func TestExportKeepsResultsForPointsTheSheetNoLongerHas(t *testing.T) {
	sh := &Sheet{Points: []Point{{ID: "A", Tag: "A", Kind: DI, Method: Observe}}}
	run := Run{ID: "r1", SheetHash: "oldhash"}
	results := []Result{
		{Run: "r1", Seq: 2, Point: "A", Outcome: Pass},
		{Run: "r1", Seq: 3, Point: "GONE", Outcome: Fail, Note: "x"},
	}
	ex := BuildExport(run, sh, results)
	if !ex.SheetChanged || len(ex.Orphans) != 1 || ex.Orphans[0].Point != "GONE" {
		t.Errorf("export %+v: want the sheet marked changed and the orphan kept", ex)
	}
	csv, err := ex.CSV()
	if err != nil || !strings.Contains(string(csv), "GONE") || !strings.Contains(string(csv), "(not on the sheet now)") {
		t.Errorf("csv %v: %s", err, csv)
	}
}

func TestExportIsDeterministic(t *testing.T) {
	sh := &Sheet{Points: []Point{{ID: "B", Tag: "B", Kind: AI, Method: Observe}, {ID: "A", Tag: "A", Kind: AI, Method: Observe}}}
	run := Run{ID: "r1", SheetHash: sh.Hash()}
	rs := []Result{{Run: "r1", Seq: 1, Point: "A", Outcome: Pass, Seen: 1.5}, {Run: "r1", Seq: 2, Point: "B", Outcome: Pass, Seen: "x"}}
	a, _ := BuildExport(run, sh, rs).JSON()
	b, _ := BuildExport(run, sh, rs).JSON()
	if !bytes.Equal(a, b) {
		t.Errorf("two builds differ")
	}
	if i, j := bytes.Index(a, []byte(`"id": "B"`)), bytes.Index(a, []byte(`"id": "A"`)); i < 0 || j < 0 || i > j {
		t.Errorf("points are not in the sheet's order (B before A):\n%s", a)
	}
}
