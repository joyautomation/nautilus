package driverconformance

import (
	"strings"
	"testing"
)

// The harness's own rules, checked with stub contracts: an expected
// failure that keeps failing passes and is logged; one that starts passing
// fails the suite; an unexpected failure fails it; a teardown that fails
// counts against the contract.
func TestHarnessVerdicts(t *testing.T) {
	passes := contract{"X1", "passes", func(tb, subject) {}}
	fatals := contract{"X2", "fatals", func(t tb, _ subject) { t.Fatalf("boom") }}
	errs := contract{"X3", "errors and carries on", func(t tb, _ subject) { t.Errorf("first"); t.Errorf("second") }}
	teardown := contract{"X4", "fails in teardown", func(t tb, _ subject) {
		t.Cleanup(func() { t.Errorf("teardown failed") })
	}}
	order := contract{"X5", "cleanups run last first", func(t tb, _ subject) {
		var got []int
		t.Cleanup(func() {
			if len(got) != 1 || got[0] != 2 {
				t.Errorf("cleanups ran %v, want the last registered first", got)
			}
		})
		t.Cleanup(func() { got = append(got, 2) })
	}}

	for _, tc := range []struct {
		c                contract
		issue            string
		wantErr, wantLog string // substrings; "" means none at all
	}{
		{c: passes},
		{c: passes, issue: "#9", wantErr: "now passes: remove its expected-failure mark (#9)"},
		{c: fatals, wantErr: "boom"},
		{c: fatals, issue: "#9", wantLog: "expected failure (#9): fatals: boom"},
		{c: errs, wantErr: "first\nsecond"},
		{c: teardown, wantErr: "teardown failed"},
		{c: order},
	} {
		t.Run(tc.c.id+tc.issue, func(t *testing.T) {
			failed, msgs := record(t, func(r tb) { tc.c.run(r, subject{}) })
			errMsg, logMsg := verdict(tc.c, tc.issue, tc.issue != "", failed, msgs)
			check := func(what, got, want string) {
				if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
					t.Errorf("%s = %q, want %q", what, got, want)
				}
			}
			check("error", errMsg, tc.wantErr)
			check("log", logMsg, tc.wantLog)
		})
	}
}

func TestHarnessValidatesMarks(t *testing.T) {
	cs := []contract{{id: "C1"}, {id: "C2"}, {id: "C5"}, {id: "C8"}}
	for _, tc := range []struct {
		name string
		s    subject
		want []string // substrings, one per expected complaint, in order
	}{
		{"clean", subject{gate: dropAndAsk, expect: map[string]string{"C1": "#865"}, unsupported: map[string]string{"C2": "no hook"}}, nil},
		{"unknown expect", subject{gate: dropAndAsk, expect: map[string]string{"C9": "#1"}}, []string{`unknown contract "C9"`}},
		{"no issue", subject{gate: dropAndAsk, expect: map[string]string{"C1": "later"}}, []string{`cites the issue`}},
		{"empty issue", subject{gate: dropAndAsk, expect: map[string]string{"C1": ""}}, []string{`cites the issue`}},
		{"unknown unsupported", subject{gate: dropAndAsk, unsupported: map[string]string{"C7": "why"}}, []string{`unsupported names unknown contract "C7"`}},
		{"no reason", subject{gate: dropAndAsk, unsupported: map[string]string{"C1": " "}}, []string{`unsupported[C1] gives no reason`}},
		{"both", subject{gate: dropAndAsk, expect: map[string]string{"C1": "#1"}, unsupported: map[string]string{"C1": "why"}}, []string{`C1 carries more than one mark: expect, unsupported`}},
		{"byDesign clean", subject{gate: holdAndDeliver, byDesign: map[string]string{"C2": "no device once loaded"}}, nil},
		{"unknown byDesign", subject{gate: dropAndAsk, byDesign: map[string]string{"C9": "why"}}, []string{`byDesign names unknown contract "C9"`}},
		{"byDesign no reason", subject{gate: dropAndAsk, byDesign: map[string]string{"C1": ""}}, []string{`byDesign[C1] gives no reason`}},
		{"byDesign and expect", subject{gate: dropAndAsk, expect: map[string]string{"C2": "#1"}, byDesign: map[string]string{"C2": "why"}}, []string{`C2 carries more than one mark: expect, byDesign`}},
		{"byDesign and unsupported", subject{gate: dropAndAsk, unsupported: map[string]string{"C2": "why"}, byDesign: map[string]string{"C2": "why"}}, []string{`C2 carries more than one mark: unsupported, byDesign`}},
		{"no gate mode", subject{}, []string{`no gate mode`}},
		{"no gate mode, C5 skipped only", subject{byDesign: map[string]string{"C5": "no outputs"}}, []string{`no gate mode`}},
		{"no gate mode, both gates skipped", subject{byDesign: map[string]string{"C5": "no outputs"}, unsupported: map[string]string{"C8": "no hook"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := validate(tc.s, cs)
			if len(got) != len(tc.want) {
				t.Fatalf("validate = %q, want %d complaint(s)", got, len(tc.want))
			}
			for i, w := range tc.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("complaint %d = %q, want it to say %q", i, got[i], w)
				}
			}
		})
	}
}
