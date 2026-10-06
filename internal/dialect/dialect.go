// Package dialect holds the vendor-semantics block libraries a project opts
// into with `dialect:` in nautilus.yaml. The default, "nautilus", is the
// runtime itself and adds nothing. "logix" adds blocks with Allen-Bradley
// semantics (TONR, …), written once in nautilus ST so the runtime runs
// them, the Logix writer emits the native instruction for them, and the
// L5X importer folds the native idiom back into them. "siemens" and
// "codesys" are reserved: accepted, empty until their libraries exist.
package dialect

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed logix/*.st
var files embed.FS

// Names are the dialects a manifest may name.
var Names = []string{"nautilus", "logix", "siemens", "codesys"}

// Known reports whether name is a dialect (the empty name is "nautilus").
func Known(name string) bool {
	if name == "" {
		return true
	}
	for _, n := range Names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// Library is one embedded source file.
type Library struct {
	// Path is the virtual project-relative path, "dialect/logix/tonr.st":
	// what diagnostics and the compose output name it.
	Path string
	ST   string
}

// Sources returns the dialect's library files, sorted by path, or an error
// for a name that is not a dialect.
func Sources(name string) ([]Library, error) {
	if !Known(name) {
		return nil, fmt.Errorf("unknown dialect %q (one of %s)", name, strings.Join(Names, ", "))
	}
	dir := strings.ToLower(name)
	if dir == "" || dir == "nautilus" {
		return nil, nil
	}
	entries, err := fs.ReadDir(files, dir)
	if err != nil {
		return nil, nil // reserved dialect, no library yet
	}
	var out []Library
	for _, e := range entries {
		raw, err := files.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Library{Path: "dialect/" + dir + "/" + e.Name(), ST: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
