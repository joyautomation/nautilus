//go:build !js

package project

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A slice is self-contained: the section, and exactly the definitions it
// reaches (drivers -> driver, devices -> device and what device names), and
// every $ref in it resolves inside the slice.
func TestSchemaSliceIsSelfContained(t *testing.T) {
	for _, section := range []string{"drivers", "driver", "devices"} {
		raw, err := SchemaSlice(section)
		if err != nil {
			t.Fatalf("%s: %v", section, err)
		}
		var s struct {
			Properties  map[string]json.RawMessage `json:"properties"`
			Definitions map[string]json.RawMessage `json:"definitions"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Properties[section]; !ok || len(s.Properties) != 1 {
			t.Fatalf("%s: properties = %v, want only the section", section, keysOf2(anyMap(s.Properties)))
		}
		for _, ref := range refsIn(string(raw)) {
			if _, ok := s.Definitions[ref]; !ok {
				t.Errorf("%s: $ref #/definitions/%s is not in the slice", section, ref)
			}
		}
	}
	raw, _ := SchemaSlice("drivers")
	var s struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	_ = json.Unmarshal(raw, &s)
	if _, ok := s.Definitions["driver"]; !ok {
		t.Error("drivers slice lacks definitions/driver")
	}
	if _, ok := s.Definitions["alarm"]; ok {
		t.Error("drivers slice carries definitions/alarm, which it never reaches")
	}
}

func anyMap(m map[string]json.RawMessage) map[string]any {
	out := map[string]any{}
	for k := range m {
		out[k] = nil
	}
	return out
}

func refsIn(s string) []string {
	var out []string
	const pre = `"$ref":"#/definitions/`
	s = strings.ReplaceAll(s, `"$ref": "`, `"$ref":"`)
	for {
		i := strings.Index(s, pre)
		if i < 0 {
			return out
		}
		s = s[i+len(pre):]
		out = append(out, s[:strings.Index(s, `"`)])
	}
}

func TestSchemaSliceUnknownSection(t *testing.T) {
	_, err := SchemaSlice("nope")
	if !errors.Is(err, ErrNoSection) || !strings.Contains(err.Error(), "drivers") {
		t.Fatalf("err = %v, want ErrNoSection naming the sections", err)
	}
	if got := SchemaSections(); len(got) < 10 {
		t.Fatalf("sections = %v", got)
	}
}
