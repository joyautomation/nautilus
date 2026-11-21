//go:build !js

package project

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/tools/vscode-iec/schemas"
)

// ErrNoSection is a schema section the manifest does not have.
var ErrNoSection = fmt.Errorf("no such manifest section")

// SchemaSections are the manifest's top-level sections the schema describes.
func SchemaSections() []string {
	var root struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schemas.Nautilus, &root); err != nil {
		return nil
	}
	out := make([]string, 0, len(root.Properties))
	for k := range root.Properties {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SchemaSlice is one manifest section as a self-contained JSON Schema: the
// section under properties, and only the definitions it reaches, so a form
// renders it without the rest of the manifest.
func SchemaSlice(section string) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(schemas.Nautilus, &root); err != nil {
		return nil, err
	}
	var props map[string]json.RawMessage
	if err := json.Unmarshal(root["properties"], &props); err != nil {
		return nil, err
	}
	prop, ok := props[section]
	if !ok {
		return nil, fmt.Errorf("%w %q (sections: %s)", ErrNoSection, section, strings.Join(SchemaSections(), ", "))
	}
	var defs map[string]json.RawMessage
	if err := json.Unmarshal(root["definitions"], &defs); err != nil {
		return nil, err
	}
	keep := map[string]json.RawMessage{}
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return
		}
		var visit func(v any)
		visit = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if ref, ok := x["$ref"].(string); ok {
					if name, ok := strings.CutPrefix(ref, "#/definitions/"); ok {
						if _, seen := keep[name]; !seen {
							if d, ok := defs[name]; ok {
								keep[name] = d
								walk(d)
							}
						}
					}
				}
				for _, c := range x {
					visit(c)
				}
			case []any:
				for _, c := range x {
					visit(c)
				}
			}
		}
		visit(v)
	}
	walk(prop)
	out := map[string]any{
		"$schema":     root["$schema"],
		"title":       section,
		"type":        "object",
		"properties":  map[string]json.RawMessage{section: prop},
		"definitions": keep,
	}
	return json.Marshal(out)
}
