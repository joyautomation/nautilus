//go:build !js

package project

import (
	"encoding/json"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func optionsOf(t *testing.T, raw []byte, path ...string) (vals, builtin []string, ok bool) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	m := dig(root, path...)
	if m == nil {
		t.Fatalf("no schema node at %v", path)
	}
	conv := func(k string) []string {
		a, has := m[k].([]any)
		if !has {
			return nil
		}
		ok = true
		out := []string{}
		for _, x := range a {
			out = append(out, x.(string))
		}
		return out
	}
	return conv("x-options"), conv("x-options-builtin"), ok
}

// The devices slice offers the profiles vendored in the project (usable
// now) and the library's other built-in ids (usable after naut devices
// add), and the files in iodd/; hidden files and links are not offered;
// nothing else in the slice changes.
func TestConfigSchemaOptions(t *testing.T) {
	fsys := fstest.MapFS{
		"profiles/mine.yaml":              {Data: []byte("x")},
		"profiles/abb-acs355-fmbt21.yaml": {Data: []byte("x")},
		"profiles/notes.txt":              {Data: []byte("x")},
		"iodd/sensor-b.xml":               {Data: []byte("x")},
		"iodd/sensor-a.xml":               {Data: []byte("x")},
		"iodd/.hidden.xml":                {Data: []byte("x")},
		"iodd/link.xml":                   {Mode: fs.ModeSymlink, Data: []byte("sensor-a.xml")},
	}
	raw, err := SchemaSlice("devices")
	if err != nil {
		t.Fatal(err)
	}
	got, err := withOptions(raw, fsys)
	if err != nil {
		t.Fatal(err)
	}
	vals, builtin, ok := optionsOf(t, got, "definitions", "device", "properties", "profile")
	if !ok || !slices.Equal(vals, []string{"abb-acs355-fmbt21", "mine"}) {
		t.Errorf("profile x-options = %v, want the vendored ids", vals)
	}
	if slices.Contains(builtin, "abb-acs355-fmbt21") || !slices.Contains(builtin, "asi-275m") {
		t.Errorf("x-options-builtin = %v: should list built-ins that are not vendored", builtin)
	}
	iodd, _, ok := optionsOf(t, got, "definitions", "device", "properties", "ports", "additionalProperties", "properties", "iodd")
	if !ok || !slices.Equal(iodd, []string{"sensor-a.xml", "sensor-b.xml"}) {
		t.Errorf("iodd x-options = %v, want the plain files of iodd/", iodd)
	}
	// An empty project still answers with lists, not null.
	empty, _ := withOptions(raw, fstest.MapFS{})
	if v, _, ok := optionsOf(t, empty, "definitions", "device", "properties", "profile"); !ok || v == nil || len(v) != 0 {
		t.Errorf("empty project: x-options = %v, want []", v)
	}
	// A slice without those fields is returned as it was.
	d, _ := SchemaSlice("drivers")
	if out, _ := withOptions(d, fsys); string(out) != string(d) || strings.Contains(string(out), "x-options") {
		t.Error("the drivers slice was changed")
	}
}

// The route's hook serves them: IOConfig.ConfigSchema reads the project the
// controller booted from.
func TestConfigSchemaOnTheRunningProject(t *testing.T) {
	files := ioFiles(0, 0, "")
	files["profiles/mine.yaml"] = &fstest.MapFile{Data: []byte("x")}
	files["iodd/s.xml"] = &fstest.MapFile{Data: []byte("x")}
	r := startIORig(t, files, false)
	raw, err := r.io.ConfigSchema("devices")
	if err != nil {
		t.Fatal(err)
	}
	if vals, _, _ := optionsOf(t, raw, "definitions", "device", "properties", "profile"); !slices.Equal(vals, []string{"mine"}) {
		t.Errorf("x-options = %v", vals)
	}
	if _, err := r.io.ConfigSchema("nope"); err == nil {
		t.Error("an unknown section was served")
	}
}
