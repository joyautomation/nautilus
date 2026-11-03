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
	// members. A glob's [ ] is a character class, so an element is named
	// with the brackets escaped.
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

func TestValidateRefusesAMalformedPattern(t *testing.T) {
	if err := (Patterns{"ok*", "["}).Validate(); err == nil {
		t.Fatal(`"[" validated`)
	}
	if err := (Patterns{"ok*", "P?01.Speed"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
