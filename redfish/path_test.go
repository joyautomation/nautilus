package redfish

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/redfish/mockup"
)

// thermalBody is a legacy Thermal resource as DMTF's public-localstorage
// mockup serves it (trimmed): MemberId strings, a disabled sensor with no
// reading, a fan with a numeric Reading.
const thermalBody = `{
  "Temperatures": [
    {"MemberId": "0", "Name": "CPU1 Temp", "ReadingCelsius": 41, "UpperThresholdCritical": 45, "Status": {"State": "Enabled", "Health": "OK"}},
    {"MemberId": "1", "Name": "CPU2 Temp", "Status": {"State": "Disabled"}},
    {"MemberId": "2", "Name": "Chassis Intake Temp", "ReadingCelsius": 25.5, "PhysicalContext": "Intake"}
  ],
  "Fans": [
    {"MemberId": "0", "Name": "BaseBoard System Fan", "Reading": 2100, "ReadingUnits": "RPM"},
    {"MemberId": "1", "Name": "BaseBoard System Fan Backup", "Reading": null}
  ],
  "Dup": [{"K": "a"}, {"K": "a"}],
  "Status": {"Health": "OK"},
  "Numbered": [{"Index": 7, "V": true}]
}`

func TestPathEval(t *testing.T) {
	doc, err := mockup.Decode([]byte(thermalBody))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want string // JSON of the values, "null" for none
	}{
		{"Status.Health", `["OK"]`},
		{"Temperatures[MemberId=0].ReadingCelsius", `[41]`},
		{"Temperatures[MemberId=2].ReadingCelsius", `[25.5]`},
		{"Temperatures[Name=Chassis Intake Temp].PhysicalContext", `["Intake"]`},
		{"Temperatures[MemberId=1].ReadingCelsius", `null`}, // disabled: no reading
		{"Temperatures[MemberId=9].ReadingCelsius", `null`}, // no such element
		{"Fans[MemberId=1].Reading", `null`},                // JSON null is absent
		{"Fans[MemberId=0].Reading", `[2100]`},
		{"Temperatures[*].ReadingCelsius", `[41,25.5]`},
		{"Numbered[Index=7].V", `[true]`}, // a numeric key matches by its string form
		{"Nope.Deeper", `null`},
		{"Status.Health.Deeper", `null`},
	}
	for _, c := range cases {
		p, err := ParsePath(c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		got, err := p.Eval(doc)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		b, _ := json.Marshal(got)
		if string(b) != c.want {
			t.Errorf("%s = %s, want %s", c.path, b, c.want)
		}
	}

	p, _ := ParsePath("Dup[K=a]")
	if _, err := p.Eval(doc); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("ambiguous selector: err = %v", err)
	}
	p, _ = ParsePath("Status[K=a]")
	if _, err := p.Eval(doc); err == nil || !strings.Contains(err.Error(), "not an array") {
		t.Errorf("selector on an object: err = %v", err)
	}
	if !mustPath(t, "Fans[*].Reading").Wildcard() || mustPath(t, "Fans[MemberId=0].Reading").Wildcard() {
		t.Error("Wildcard")
	}
}

func mustPath(t *testing.T, s string) *Path {
	t.Helper()
	p, err := ParsePath(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPathParseErrors(t *testing.T) {
	cases := map[string]string{
		"":                     "empty",
		"Fans[0].Reading":      "positional index",
		"Fans[MemberId=0":      "unclosed",
		"Fans.":                "trailing",
		".Fans":                "empty property",
		"Fans[=0]":             "want [Key=Value]",
		"Fans[MemberId=0]x":    "expected '.'",
		"Fans]":                "unexpected ']'",
		"Fans[Member Id=0]":    "one property name",
		"Fans..Reading":        "empty property",
		" Status":              "surrounding spaces",
		"Fans[Location.X=1].R": "one property name",
	}
	for src, want := range cases {
		_, err := ParsePath(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParsePath(%q) = %v, want an error containing %q", src, err, want)
		}
	}
}
