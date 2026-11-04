package writable

import "testing"

func TestAllowsAMatchAndEverythingBelowIt(t *testing.T) {
	ps := Patterns{"*_SP", "Pump*.SpeedSP", "Motor1"}
	cases := map[string]bool{
		"TempSP":             false,
		"Temp_SP":            true,
		"Pump1.SpeedSP":      true,
		"Pump1.SpeedSP.Hi":   true, // below a matched member
		"Pump1.Running":      false,
		"Motor1":             true,
		"Motor1.START":       true, // a matched tag opens its members
		"Motor1.Drive.Speed": true,
		"Motor10":            false,
		"P101.Drive.Speed":   false,
	}
	for p, want := range cases {
		if got := ps.Allows(p); got != want {
			t.Errorf("Allows(%q) = %v, want %v", p, got, want)
		}
	}
	// Array elements are below their array: a pattern on an array of
	// structs opens every element and member, one on an element its
	// members. Escaped brackets name the same element as bare ones.
	ps = Patterns{"Drives", `Pumps\[2\]`, "Line.Steps"}
	cases = map[string]bool{
		"Drives[0]":            true,
		"Drives[0].Speed":      true,
		"Drives[0].Inner[3].X": true,
		"Pumps[2].SpeedSP":     true,
		"Pumps[1].SpeedSP":     false,
		"Line.Steps[4].Mode":   true,
		"Line.Other[0]":        false,
		"DrivesB[0].Speed":     false,
	}
	for p, want := range cases {
		if got := ps.Allows(p); got != want {
			t.Errorf("Allows(%q) = %v, want %v", p, got, want)
		}
	}
	if (Patterns{}).Allows("Anything") {
		t.Error("an empty list opens something")
	}
}

// [n] in a pattern is that array index, literally, and [*] any index:
// "Counts[2]" names the element Counts[2] (never Counts2, as a glob's
// character class would), "Counts[*]" every element, "Drives[*].Speed"
// every element's Speed. * and ? keep their glob meaning elsewhere.
func TestArrayIndexPatterns(t *testing.T) {
	cases := []struct {
		pat  string
		p    string
		want bool
	}{
		{"Counts[2]", "Counts[2]", true},
		{"Counts[2]", "Counts2", false},
		{"Counts[2]", "Counts[3]", false},
		{"Counts[2]", "Counts[22]", false},
		{`Counts\[2\]`, "Counts[2]", true},
		{"Counts[*]", "Counts[0]", true},
		{"Counts[*]", "Counts[1023]", true},
		{"Counts[*]", "Counts[-5]", true}, // an array with a negative lower bound
		{"Counts[*]", "Counts", false},
		{"Counts[*]", "Counts[0].X", false}, // exact: the member is below it (Allows)
		{"Counts[*]", "Counts[a]", false},
		{"Offs[-1]", "Offs[-1]", true},
		{"Drives[*].Speed", "Drives[0].Speed", true},
		{"Drives[*].Speed", "Drives[7].Speed", true},
		{"Drives[*].Speed", "Drives[7].Torque", false},
		{"Drives[*].Speed", "Drives[0].X[1].Speed", false}, // [*] is one index, not any run
		{"M[*][*]", "M[1][2]", true},
		{"Plc*.Inputs[17]", "PlcLabConnector.Inputs[17]", true},
		{"Plc*.Inputs[17]", "PlcLabConnector.Inputs[7]", false},
		{"*_SP", "Temp_SP", true},
		{"P?01", "P101", true},
		{"Pump*.SpeedSP", "Pump1.SpeedSP", true},
		{"a*", "a/b", false}, // * does not cross / (sparkplug metric names)
		{"Tank*", "Tänk1", false},
		{"Tänk*", "Tänk1", true},
	}
	for _, c := range cases {
		if got := Matches(c.pat, c.p); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.pat, c.p, got, c.want)
		}
	}
	// Allows: an element pattern opens that element and what is below it,
	// nothing beside it.
	ps := Patterns{"Counts[2]", "Drives[*].Speed", "Inputs[*]"}
	for p, want := range map[string]bool{
		"Counts[2]":          true,
		"Counts[3]":          false,
		"Counts":             false,
		"Drives[3].Speed":    true,
		"Drives[3].Speed.Hi": true,
		"Drives[3].Torque":   false,
		"Inputs[17]":         true,
		"Inputs":             false, // the array itself is not an element
	} {
		if got := ps.Allows(p); got != want {
			t.Errorf("Allows(%q) = %v, want %v", p, got, want)
		}
	}
}

// A [ that is not an array index is refused: a pattern has no character
// classes, so "[abc]" says nothing a tag path can hold.
func TestValidateRefusesAMalformedPattern(t *testing.T) {
	for _, bad := range []string{"[", "Counts[", "Counts[a]", "Counts[a-z]", "Counts[]", "Counts[1,2]", "Counts[ 1]", "Counts[1?]", `trailing\`} {
		if err := (Patterns{"ok*", bad}).Validate(); err == nil {
			t.Errorf("%q validated", bad)
		}
		if Matches(bad, bad) {
			t.Errorf("malformed %q matched", bad)
		}
	}
	if err := (Patterns{"ok*", "P?01.Speed", "Counts[2]", "Counts[*]", "Drives[*].Speed", `Pumps\[2\]`, "Offs[-3]"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
