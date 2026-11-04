package main

import (
	"reflect"
	"strings"
	"testing"
)

func goodScore() *Score {
	return &Score{
		Compiles: true,
		Hidden:   TestResult{Total: 5, Passed: 5},
	}
}

func TestJudge(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Score)
		want   Verdict // Reasons compared by prefix below
		reason string  // a substring of some reason, when not Good
	}{
		{"all good", func(*Score) {}, Verdict{Correct: true, Clean: true, Structured: true, Good: true}, ""},
		{"compile error", func(s *Score) {
			s.Compiles = false
			s.Check.Errors = 2
			s.Hidden = TestResult{Error: "compile: undefined"}
		}, Verdict{Structured: true}, "does not compile (2 errors)"},
		{"a hidden test fails", func(s *Score) {
			s.Hidden = TestResult{Total: 5, Passed: 4, Failures: []string{"x"}}
		}, Verdict{Clean: true, Structured: true}, "hidden tests 4/5"},
		{"no hidden test ran", func(s *Score) {
			s.Hidden = TestResult{Error: "undefined tag Horn"}
		}, Verdict{Clean: true, Structured: true}, "hidden tests did not run: undefined tag Horn"},
		{"wrong language", func(s *Score) { s.MissingLanguages = []string{"ld"} },
			Verdict{Clean: true, Structured: true}, "no ld program is a task"},
		{"a warning", func(s *Score) { s.Check.Warnings = 1 },
			Verdict{Correct: true, Structured: true}, "1 check warnings"},
		{"a structure violation", func(s *Score) { s.Violations = []string{"ld.series>6 tank.ld"} },
			Verdict{Correct: true, Clean: true}, "structure: ld.series>6 tank.ld"},
	}
	for _, c := range cases {
		s := goodScore()
		c.mutate(s)
		got := Judge(s)
		reasons := got.Reasons
		got.Reasons = nil
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if c.reason == "" {
			if len(reasons) != 0 {
				t.Errorf("%s: a Good verdict carries reasons %v", c.name, reasons)
			}
			continue
		}
		if !strings.Contains(strings.Join(reasons, "|"), c.reason) {
			t.Errorf("%s: reasons %v lack %q", c.name, reasons, c.reason)
		}
	}
}
