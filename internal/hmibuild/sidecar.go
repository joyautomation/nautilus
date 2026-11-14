package hmibuild

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// A project's own component declares its pipe ports in a
// {Name}.component.json, which the mimic editor and naut check read. The
// kit's Mimic, at runtime, only knows an instance's own `ports` and its
// built-in table, so a screen naut builds gets each sidecar's ports
// written onto the equipment that has none of its own: the pipes the
// editor drew are the pipes the screen draws (bw01 fable-2, haiku-1,
// opus-3 and sonnet-3 drew every anchored pipe zero-length without it).

// sidecarFiles maps a component's name to its {Name}.component.json (the
// whole object), the one the editor reads: anywhere in the project, and
// when two directories declare one name, the shorter path wins, ties
// broken by name (mimicComponentIndex.ts). A file that isn't a JSON
// object doesn't count.
func sidecarFiles(project string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	winner := map[string]string{}
	_ = filepath.WalkDir(project, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != project && skipDir(p) {
				return filepath.SkipDir
			}
			return nil
		}
		n := d.Name()
		name := strings.TrimSuffix(n, ".component.json")
		if name == n || name == "" {
			return nil
		}
		rel, _ := filepath.Rel(project, p)
		rel = filepath.ToSlash(rel)
		if w, ok := winner[name]; ok && (len(w) < len(rel) || len(w) == len(rel) && w < rel) {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil || obj == nil {
			return nil
		}
		winner[name] = rel
		out[name] = json.RawMessage(bytes.TrimSpace(raw))
		return nil
	})
	return out
}

// sidecarPorts maps a component's name to the `ports` its
// {Name}.component.json declares (sidecarFiles), when it declares any.
func sidecarPorts(project string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for name, raw := range sidecarFiles(project) {
		var sc struct {
			Ports json.RawMessage `json:"ports"`
		}
		if json.Unmarshal(raw, &sc) != nil || len(sc.Ports) == 0 || bytes.Equal(sc.Ports, []byte("null")) {
			continue
		}
		out[name] = sc.Ports
	}
	return out
}

// withSidecarPorts writes each sidecar's ports onto the mimic's equipment
// of that component that has no `ports` of its own (an instance's own,
// even an empty list, is left as it is). A doc it can't read, or one with
// nothing to add, comes back unchanged.
func withSidecarPorts(raw []byte, ports map[string]json.RawMessage) []byte {
	if len(ports) == 0 {
		return raw
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return raw
	}
	var equipment []map[string]json.RawMessage
	if json.Unmarshal(doc["equipment"], &equipment) != nil {
		return raw
	}
	changed := false
	for _, eq := range equipment {
		if eq == nil {
			continue
		}
		if _, own := eq["ports"]; own {
			continue
		}
		var c string
		if json.Unmarshal(eq["component"], &c) != nil || builtinEquipment[c] {
			continue
		}
		if p, ok := ports[c]; ok {
			eq["ports"] = p
			changed = true
		}
	}
	if !changed {
		return raw
	}
	eqs, err := json.Marshal(equipment)
	if err != nil {
		return raw
	}
	doc["equipment"] = eqs
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}
