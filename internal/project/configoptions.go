//go:build !js

package project

import (
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/devprofile"
)

// Option lists on the config screens' schema slices (#711). The manifest
// schema cannot enumerate what only the project knows, so the schema route
// adds them as `x-options` (values usable now) and `x-options-builtin`
// (values a `naut devices add` makes usable) on the field that takes them:
// devices[].profile (profiles/<id>.yaml vendored in the project, and the
// device library's built-in ids) and devices[].ports.*.iodd (the files in
// the project's iodd/ folder). They are suggestions, not an enum: a form
// still accepts any text and the loader says what is wrong with it.

// withOptions adds the project's option lists to a schema slice. A slice
// that has none of those fields comes back untouched.
func withOptions(raw []byte, fsys fs.FS) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	dev := dig(root, "definitions", "device", "properties")
	if dev == nil {
		return raw, nil
	}
	if prof, ok := dev["profile"].(map[string]any); ok {
		vendored := dirIDs(fsys, "profiles", ".yaml")
		prof["x-options"] = vendored
		have := map[string]bool{}
		for _, v := range vendored {
			have[v] = true
		}
		builtin := []string{}
		if ps, err := devprofile.Builtin(); err == nil {
			for _, p := range ps {
				if !have[p.ID] {
					builtin = append(builtin, p.ID)
				}
			}
		}
		prof["x-options-builtin"] = builtin
	}
	if iodd := dig(dev, "ports", "additionalProperties", "properties", "iodd"); iodd != nil {
		iodd["x-options"] = dirIDs(fsys, "iodd", "")
	}
	return json.Marshal(root)
}

// dig follows keys through nested objects; nil when the way is not there.
func dig(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		next, ok := m[k].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

// dirIDs lists the regular files in dir with the given extension ("" for
// any), by name without the extension when one was asked for, sorted. A
// symbolic link is skipped, as everywhere the screens read the project.
func dirIDs(fsys fs.FS, dir, ext string) []string {
	ents, err := fs.ReadDir(fsys, dir)
	out := []string{}
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if ext != "" {
			if path.Ext(e.Name()) != ext {
				continue
			}
			out = append(out, strings.TrimSuffix(e.Name(), ext))
		} else {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// ConfigSchema is SchemaSlice with the project's option lists on it.
func (io *IOConfig) ConfigSchema(section string) ([]byte, error) {
	raw, err := SchemaSlice(section)
	if err != nil {
		return nil, err
	}
	return withOptions(raw, io.bootFS)
}
