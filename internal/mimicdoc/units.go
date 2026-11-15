// Package mimicdoc reads a *.mimic.json the way the kit's <Mimic> draws
// it. Its one job today: drawing out a document's units (a filter cell
// repeated four times, written once), so naut check and naut's HMI build
// see the equipment, pipes and labels the screen will show. The kit's
// expandUnits (hmi/src/lib/mimic.ts) does the same at runtime; the
// fixture in testdata/mimic-units holds the two to one answer.
package mimicdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type unit struct {
	ID        string            `json:"id"`
	Each      []json.RawMessage `json:"each"`
	As        string            `json:"as"`
	Step      []float64         `json:"step"`
	Equipment []map[string]any  `json:"equipment"`
	Pipes     []map[string]any  `json:"pipes"`
	Labels    []map[string]any  `json:"labels"`
}

// ExpandUnits returns the document with its units drawn out: each unit's
// equipment, pipes and labels once per value of `each`, `{n}` (or `{as}`)
// filled in, the i-th copy moved by step × i, after the document's own;
// `units` is gone. A document with no units, or one it can't read, comes
// back unchanged with changed false (a document that isn't JSON is for the
// caller's own parse to report).
func ExpandUnits(raw []byte) (out []byte, changed bool, err error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return raw, false, nil
	}
	ur, ok := doc["units"]
	if !ok {
		return raw, false, nil
	}
	var units []unit
	if err := decode(ur, &units); err != nil {
		return raw, false, fmt.Errorf("units: %v", err)
	}
	lists := map[string][]any{}
	for _, k := range []string{"equipment", "pipes", "labels"} {
		if r, ok := doc[k]; ok {
			var l []any
			if err := decode(r, &l); err != nil {
				return raw, false, fmt.Errorf("%s: %v", k, err)
			}
			lists[k] = l
		}
		if lists[k] == nil {
			lists[k] = []any{}
		}
	}
	for ui, u := range units {
		ph := "{" + u.As + "}"
		if u.As == "" {
			ph = "{n}"
		}
		var dx, dy float64
		if len(u.Step) == 2 {
			dx, dy = u.Step[0], u.Step[1]
		}
		if len(u.Each) == 0 {
			continue
		}
		for i, rv := range u.Each {
			var v any
			if err := decode(rv, &v); err != nil {
				return raw, false, fmt.Errorf("units[%d].each[%d]: %v", ui, i, err)
			}
			ox, oy := dx*float64(i), dy*float64(i)
			for _, e := range u.Equipment {
				c := fill(e, ph, v).(map[string]any)
				c["x"] = shift(c["x"], ox)
				c["y"] = shift(c["y"], oy)
				lists["equipment"] = append(lists["equipment"], c)
			}
			for _, p := range u.Pipes {
				c := fill(p, ph, v).(map[string]any)
				pts, _ := c["points"].([]any)
				moved := make([]any, 0, len(pts))
				for _, pt := range pts {
					xy, _ := pt.([]any)
					if len(xy) == 2 {
						moved = append(moved, []any{shift(xy[0], ox), shift(xy[1], oy)})
					} else {
						moved = append(moved, pt)
					}
				}
				c["points"] = moved
				lists["pipes"] = append(lists["pipes"], c)
			}
			for _, l := range u.Labels {
				c := fill(l, ph, v).(map[string]any)
				c["x"] = shift(c["x"], ox)
				c["y"] = shift(c["y"], oy)
				lists["labels"] = append(lists["labels"], c)
			}
		}
	}
	delete(doc, "units")
	for k, l := range lists {
		b, err := json.Marshal(l)
		if err != nil {
			return raw, false, err
		}
		doc[k] = b
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return raw, false, err
	}
	return b, true, nil
}

func decode(r json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(r))
	d.UseNumber()
	return d.Decode(v)
}

// fill puts v in for the placeholder in every string under x; a string
// that is exactly the placeholder becomes v itself (a number stays one).
func fill(x any, ph string, v any) any {
	switch t := x.(type) {
	case string:
		if t == ph {
			return v
		}
		return strings.ReplaceAll(t, ph, text(v))
	case []any:
		out := make([]any, len(t))
		for i, y := range t {
			out[i] = fill(y, ph, v)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, y := range t {
			out[k] = fill(y, ph, v)
		}
		return out
	}
	return x
}

func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	}
	return fmt.Sprint(v)
}

// shift adds d to a coordinate (absent is 0), as a JSON number.
func shift(c any, d float64) any {
	var f float64
	switch t := c.(type) {
	case json.Number:
		f, _ = t.Float64()
	case float64:
		f = t
	}
	return json.Number(strconv.FormatFloat(f+d, 'f', -1, 64))
}
