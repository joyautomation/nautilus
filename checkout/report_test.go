package checkout

import (
	"os"
	"strings"
	"testing"
)

// A note is a person's words: the report escapes them, loads nothing, and
// says plainly whether the run is signed off.
func TestReportEscapesAndLoadsNothing(t *testing.T) {
	sh := &Sheet{Title: `Plant <b>I/O</b>`, Points: []Point{{ID: "A", Tag: `A"><script>x</script>`, Kind: DI, Method: Observe}}}
	run := Run{ID: "r1", SheetHash: sh.Hash(), By: "alice"}
	rs := []Result{{Run: "r1", Seq: 2, Point: "A", Outcome: Fail, Note: `<img src=x onerror=alert(1)>`, By: "alice", Seen: map[string]any{"a": "<i>"}}}
	page, err := Report{Export: BuildExport(run, sh, rs)}.HTML()
	if err != nil {
		t.Fatal(err)
	}
	h := string(page)
	for _, bad := range []string{"<script", "<img", "<b>I/O", "<link", "<iframe", "<i>", " src=\"", "href=", "http://", "https://"} {
		if strings.Contains(h, bad) {
			t.Errorf("the report contains %q:\n%s", bad, h)
		}
	}
	for _, want := range []string{"DRAFT, NOT SIGNED OFF", "&lt;img", SealNotice[:40], "not by a personal certificate"} {
		if !strings.Contains(strings.ReplaceAll(h, "&#39;", "'"), want) {
			t.Errorf("the report lacks %q", want)
		}
	}
	if strings.Contains(h, `class="banner sealed"`) {
		t.Errorf("a run that is not sealed says it is")
	}
	if strings.Contains(h, "legally binding") || strings.Contains(h, "21 CFR") {
		t.Errorf("the report claims a legal standing")
	}
}

func TestReportOfASealedRunNamesWhoSignedAndWhatItWasConnectedTo(t *testing.T) {
	s, _, run := sealedStore(t)
	defer s.Close()
	in := sealIn(run, "bob")
	in.Note = "walked down with the contractor"
	in.Evidence.Devices = []DeviceEvidence{{Name: "rio1", Endpoint: "10.0.0.11:502", Class: "network"}}
	if _, err := s.Seal(in); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(s.SealedPath(run))
	rep, err := ReportFromSealed(b)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := rep.HTML()
	h := string(page)
	for _, want := range []string{"SEALED: signed off by bob", "two-person", "user:alice", "walked down with the contractor", "rio1", "10.0.0.11:502", "network", "modbus", s.KeyFingerprint(), rep.Seal.ExportSha256, "naut checkout verify"} {
		if !strings.Contains(h, want) {
			t.Errorf("the sealed report lacks %q", want)
		}
	}
	if strings.Contains(h, "DRAFT") {
		t.Errorf("a sealed run's report says it is a draft")
	}
	// the report is made from the export the signature covers: every point's results are in it
	for _, id := range []string{"XV101", "LSH101", "FIT201"} {
		if !strings.Contains(h, id) {
			t.Errorf("the report lacks point %s", id)
		}
	}
}

// A sealed record signed by another key than the controller's current one is
// shown as such: the page's SEALED is a signature check, not a statement that
// the key is this controller's.
func TestReportSaysWhenTheKeyIsNotTheControllersCurrentOne(t *testing.T) {
	s, _, run := sealedStore(t)
	defer s.Close()
	if _, err := s.Seal(sealIn(run, "bob")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(s.SealedPath(run))
	rep, _ := ReportFromSealed(b)
	rep.CurrentKey = s.KeyFingerprint()
	page, _ := rep.HTML()
	if strings.Contains(string(page), "not this controller") {
		t.Errorf("a record signed by the current key carries a warning")
	}
	rep.CurrentKey = "00000000-00000000-00000000-00000000"
	page, _ = rep.HTML()
	if !strings.Contains(string(page), "not this controller&#39;s current signing key") && !strings.Contains(string(page), "not this controller's current signing key") {
		t.Errorf("a record signed by another key carries no warning:\n%s", page)
	}
}
