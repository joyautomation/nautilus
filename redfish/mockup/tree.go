// Package mockup is the Redfish stand-in: a recorded service as a Tree of
// resources, read from and written to the DMTF mockup directory layout,
// and an HTTP server that serves one (for the driver's tests and `naut
// redfish serve`). docs/design/it-drivers.md §8–9.
//
// The layout is DMTF's (DSP2043, and what Redfish-Mockup-Server serves):
// one directory per resource with its body in index.json. Two forms exist
// and both load — the "tall" form keeps the URI prefix on disk
// (<dir>/redfish/v1/Chassis/1/index.json); the "short" form DMTF's
// published bundle uses drops it (<dir>/index.json is the service root,
// <dir>/Chassis/1/index.json the chassis). `browse --record` writes the
// short form, so a recording and a DMTF bundle mockup are the same kind of
// thing and Redfish-Mockup-Server serves either with --short-form.
package mockup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Root is the service root URI every Tree is rooted at.
const Root = "/redfish/v1"

// Tree is a recorded service: resource URI ("/redfish/v1/Chassis/1") →
// its JSON body, exactly as served.
type Tree map[string]json.RawMessage

// LoadDir reads a mockup directory in either form.
func LoadDir(dir string) (Tree, error) {
	base := dir
	if st, err := os.Stat(filepath.Join(dir, "redfish", "v1", "index.json")); err == nil && !st.IsDir() {
		base = filepath.Join(dir, "redfish", "v1")
	} else if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		return nil, fmt.Errorf("%s: not a Redfish mockup (no index.json, and no redfish/v1/index.json)", dir)
	}
	t := Tree{}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "index.json" {
			return nil
		}
		rel, err := filepath.Rel(base, filepath.Dir(p))
		if err != nil {
			return err
		}
		uri := Root
		if rel != "." {
			uri += "/" + filepath.ToSlash(rel)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return fmt.Errorf("%s: not valid JSON", p)
		}
		t[uri] = json.RawMessage(raw)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := t[Root]; !ok {
		return nil, fmt.Errorf("%s: no service root", dir)
	}
	return t, nil
}

// WriteDir writes the tree in the short DMTF form, bodies re-indented so a
// recording diffs line by line. The directory must not already hold a
// mockup: a recording is written once and committed, never merged.
func (t Tree) WriteDir(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err == nil {
		return fmt.Errorf("%s already holds a mockup — record into an empty directory", dir)
	}
	for _, uri := range t.URIs() {
		rel, ok := RelPath(uri)
		if !ok {
			return fmt.Errorf("%s: outside %s", uri, Root)
		}
		d := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, t[uri], "", "    "); err != nil {
			return fmt.Errorf("%s: %w", uri, err)
		}
		buf.WriteByte('\n')
		if err := os.WriteFile(filepath.Join(d, "index.json"), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RelPath maps a URI to its directory under the short form ("" for the
// root). ok is false for a URI outside /redfish/v1 or one that would
// escape the directory.
func RelPath(uri string) (string, bool) {
	uri = strings.TrimSuffix(uri, "/")
	if uri == Root {
		return "", true
	}
	rest, ok := strings.CutPrefix(uri, Root+"/")
	if !ok || rest == "" {
		return "", false
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `\#?`) {
			return "", false
		}
	}
	return rest, true
}

// URIs lists the tree's resources, sorted.
func (t Tree) URIs() []string {
	out := make([]string, 0, len(t))
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ErrNotFound: the tree has no such resource.
var ErrNotFound = errors.New("no such resource")

// Get decodes one resource (numbers as json.Number, like the driver).
func (t Tree) Get(uri string) (map[string]any, error) {
	raw, ok := t[Normalize(uri)]
	if !ok {
		return nil, fmt.Errorf("%s: %w", uri, ErrNotFound)
	}
	return Decode(raw)
}

// Decode parses a resource body with json.Number preserved, so an integer
// counter survives exactly and a reading keeps the form it was served in.
func Decode(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// Normalize trims a trailing slash (the root is served both ways).
func Normalize(uri string) string {
	if len(uri) > 1 {
		uri = strings.TrimSuffix(uri, "/")
	}
	return uri
}

// Links lists every @odata.id a body references, in document order —
// what a recording walk follows.
func Links(doc any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if id, ok := x["@odata.id"].(string); ok {
				out = append(out, id)
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(x[k])
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
	return out
}
