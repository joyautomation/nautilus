package main

import "fmt"

// Verdict is one run's pass/fail, split so a report can say which part
// failed. Good is the single verdict: the run did what the spec asked, in
// the language it asked for, without a warning, and its diagrams follow
// AGENTS.md's structure rules.
type Verdict struct {
	// Correct: compiles, the required languages are task programs, and
	// every hidden acceptance test passes.
	Correct bool `json:"correct"`
	// Clean: compiles and naut check printed no warnings.
	Clean bool `json:"clean"`
	// Structured: no structure rule broken in any LD/FBD/SFC file.
	Structured bool     `json:"structured"`
	Good       bool     `json:"good"`
	Reasons    []string `json:"reasons,omitempty"` // why it is not Good
}

// Judge derives the verdict from a score. It reads nothing else, so it is
// deterministic and unit-tested on hand-built scores.
func Judge(s *Score) Verdict {
	var v Verdict
	var why []string
	if !s.Compiles {
		why = append(why, fmt.Sprintf("does not compile (%d errors)", s.Check.Errors))
	}
	for _, l := range s.MissingLanguages {
		why = append(why, "no "+l+" program is a task")
	}
	hiddenOK := s.Hidden.Total > 0 && s.Hidden.Passed == s.Hidden.Total
	switch {
	case s.Hidden.Total == 0:
		msg := "hidden tests did not run"
		if s.Hidden.Error != "" {
			msg += ": " + firstLine(s.Hidden.Error)
		}
		why = append(why, msg)
	case !hiddenOK:
		why = append(why, fmt.Sprintf("hidden tests %d/%d", s.Hidden.Passed, s.Hidden.Total))
	}
	v.Correct = s.Compiles && len(s.MissingLanguages) == 0 && hiddenOK

	v.Clean = s.Compiles && s.Check.Warnings == 0
	if s.Check.Warnings > 0 {
		why = append(why, fmt.Sprintf("%d check warnings", s.Check.Warnings))
	}

	v.Structured = len(s.Violations) == 0
	for _, x := range s.Violations {
		why = append(why, "structure: "+x)
	}

	v.Good = v.Correct && v.Clean && v.Structured
	if !v.Good {
		v.Reasons = why
	}
	return v
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
