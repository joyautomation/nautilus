package main

// manifestEdit is how a command writes to nautilus.yaml (#708: naut auth
// init, naut opcua trust --as): the file is a person's, with their
// comments, blank lines and layout, so a command never re-encodes it. It
// finds where to write with the parsed YAML's line numbers and splices
// text in there; every byte outside the insert stays as it was. Before the
// file is replaced, the edited text must load as a manifest (the same
// decoder and auth: compile naut check uses), so a command can never leave
// a project that no longer loads.

import (
	"bytes"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing/fstest"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/project"
)

type manifestEdit struct {
	dir  string // the project directory
	name string // the manifest, relative to dir (nautilus.yaml, or -m's)
	path string
	raw  []byte
	nl   string     // the file's line ending: "\n" or "\r\n"
	root *yaml.Node // the top-level mapping
}

// openManifestEdit reads dir's manifest (name "" = nautilus.yaml) for an
// edit. One that does not load now is refused: the edit's own check could
// not tell its mistakes from the file's.
func openManifestEdit(dir, name string) (*manifestEdit, error) {
	if name == "" {
		name = project.ManifestName
	}
	e := &manifestEdit{dir: dir, name: name, path: filepath.Join(dir, filepath.FromSlash(name)), nl: "\n"}
	var err error
	if e.raw, err = os.ReadFile(e.path); err != nil {
		return nil, err
	}
	if bytes.Contains(e.raw, []byte("\r\n")) {
		e.nl = "\r\n"
	}
	if _, err := project.ReadManifest(os.DirFS(dir), name); err != nil {
		return nil, fmt.Errorf("%w\n(fix that first: a command edits only a manifest that loads)", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(e.raw, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: not a mapping at the top", e.path)
	}
	e.root = doc.Content[0]
	return e, nil
}

// child is key's key and value nodes in the mapping m, nil when absent.
func child(m *yaml.Node, key string) (k, v *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// blockMapping reports whether v is a mapping written as an indented block
// (a command can add a line to it), not {flow} style or a scalar.
func blockMapping(v *yaml.Node) bool {
	return v != nil && v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0
}

// childIndent is the indentation of a block mapping's entries.
func childIndent(v *yaml.Node) string {
	return strings.Repeat(" ", v.Content[0].Column-1)
}

// insertUnder adds lines (each without its line ending, indented relative
// to the mapping's entries) as the first entries of the block mapping
// whose key is k, right after k's line.
func (e *manifestEdit) insertUnder(k, v *yaml.Node, lines []string) error {
	if !blockMapping(v) {
		return fmt.Errorf("%s: %s (line %d) is not a block mapping a command can add a line to", e.name, k.Value, k.Line)
	}
	ind := childIndent(v)
	if v.Content[0].Line <= k.Line {
		return fmt.Errorf("%s: %s (line %d) has an entry on its own line; a command adds lines only under a key that ends its line", e.name, k.Value, k.Line)
	}
	var ins strings.Builder
	for _, l := range lines {
		ins.WriteString(ind + l + e.nl)
	}
	return e.insertAfterLine(k.Line, ins.String())
}

// insertAfterLine splices text (whole lines, each ending in the file's
// line ending) in after line n (1-based).
func (e *manifestEdit) insertAfterLine(n int, text string) error {
	off := 0
	for i := 0; i < n; i++ {
		j := bytes.IndexByte(e.raw[off:], '\n')
		if j < 0 {
			if i == n-1 { // the last line, with no line ending
				e.raw = append(e.raw, e.nl...)
				off = len(e.raw)
				break
			}
			return fmt.Errorf("%s has no line %d", e.name, n)
		}
		off += j + 1
	}
	e.raw = append(e.raw[:off:off], append([]byte(text), e.raw[off:]...)...)
	return nil
}

// appendBlock adds text (whole lines, "\n" endings) at the end of the
// file, after a blank line.
func (e *manifestEdit) appendBlock(text string) {
	if len(e.raw) > 0 && !bytes.HasSuffix(e.raw, []byte("\n")) {
		e.raw = append(e.raw, e.nl...)
	}
	if len(e.raw) > 0 && !bytes.HasSuffix(e.raw, []byte("\n\n")) && !bytes.HasSuffix(e.raw, []byte("\r\n\r\n")) {
		e.raw = append(e.raw, e.nl...)
	}
	e.raw = append(e.raw, strings.ReplaceAll(text, "\n", e.nl)...)
}

// check loads the edited text as the manifest, the rest of the project as
// it is on disk.
func (e *manifestEdit) check() (*project.Manifest, error) {
	return project.ReadManifest(overlayFS{base: os.DirFS(e.dir), name: e.name, data: e.raw}, e.name)
}

// save checks the edited text and, only when it loads, replaces the file:
// a temporary file beside it, synced, renamed over it, its mode kept.
func (e *manifestEdit) save() (*project.Manifest, error) {
	m, err := e.check()
	if err != nil {
		return nil, fmt.Errorf("the edit would not load, so %s is unchanged: %w", e.path, err)
	}
	mode := iofs.FileMode(0o644)
	if st, err := os.Stat(e.path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(e.path), ".nautilus-edit-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // gone after the rename; cleans up a failure
	_, err = tmp.Write(e.raw)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), e.path)
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// overlayFS is base with one file's content replaced.
type overlayFS struct {
	base iofs.FS
	name string
	data []byte
}

func (o overlayFS) Open(name string) (iofs.File, error) {
	if name == o.name {
		return fstest.MapFS{name: {Data: o.data, Mode: 0o644}}.Open(name)
	}
	return o.base.Open(name)
}

func (o overlayFS) ReadFile(name string) ([]byte, error) {
	if name == o.name {
		return append([]byte(nil), o.data...), nil
	}
	return iofs.ReadFile(o.base, name)
}

// errNoAuth is a command's refusal to create an auth: block as a side
// effect: one turns a controller with none (open) into one where
// everything needs a credential.
var errNoAuth = errors.New("the manifest has no auth: block: run naut auth init first (adding one closes the controller to anyone without a credential, so no other command does it on the side)")

// restartNote ends every command that changes the manifest's auth: or
// server: (#708): nothing reloads them live.
const restartNote = "restart naut run to apply (a built controller: build and deploy it again)"
