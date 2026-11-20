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
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/project"
)

type manifestEdit struct {
	dir  string // the project directory
	name string // the manifest, relative to dir (nautilus.yaml, or -m's)
	path string
	raw  []byte
	nl   string            // the file's line ending: "\n" or "\r\n"
	root *yaml.Node        // the top-level mapping
	m    *project.Manifest // the manifest before the edit
	orig []byte            // the file as read, to see a change made meanwhile
}

// refuseOddBreaks is oddBreaks; a test turns it off to show the exact
// check after the edit refuses a misplaced one by itself.
var refuseOddBreaks = oddBreaks

// oddBreaks refuses a line break other than \n and \r\n. YAML counts a
// lone \r, U+0085, U+2028 and U+2029 as line breaks too, so its line
// numbers and the file's \n lines disagree after one, and an edit placed
// by line would land in the wrong place.
func oddBreaks(raw []byte) error {
	for i := 0; i < len(raw); i++ {
		var what string
		switch {
		case raw[i] == '\r' && (i+1 == len(raw) || raw[i+1] != '\n'):
			what = "a carriage return not followed by a newline"
		case bytes.HasPrefix(raw[i:], []byte("\u0085")):
			what = "U+0085 (next line)"
		case bytes.HasPrefix(raw[i:], []byte("\u2028")):
			what = "U+2028 (line separator)"
		case bytes.HasPrefix(raw[i:], []byte("\u2029")):
			what = "U+2029 (paragraph separator)"
		default:
			continue
		}
		line := bytes.Count(raw[:i], []byte("\n")) + 1
		return fmt.Errorf("line %d holds %s, which YAML reads as a line break: a command cannot place an edit around it; replace it with a plain newline (or remove it)", line, what)
	}
	return nil
}

// sameBut reports whether post is pre except for what clear blanks in
// both: every other key of the manifest decodes the same. A command's
// edit is exact when this holds and the cleared part is what it meant.
func sameBut(pre, post *project.Manifest, clear func(*project.Manifest)) bool {
	a, b := *pre, *post
	clear(&a)
	clear(&b)
	ya, err1 := yaml.Marshal(&a)
	yb, err2 := yaml.Marshal(&b)
	return err1 == nil && err2 == nil && bytes.Equal(ya, yb)
}

// openManifestEdit reads dir's manifest (name "" = nautilus.yaml) for an
// edit. One that does not load now is refused: the edit's own check could
// not tell its mistakes from the file's.
func openManifestEdit(dir, name string) (*manifestEdit, error) {
	if name == "" {
		name = project.ManifestName
	}
	// Cleaned as ReadManifest cleans it ("./nautilus.yaml" is
	// "nautilus.yaml"), so the overlay check reads the edited text and not
	// the file on disk.
	name = path.Clean(filepath.ToSlash(name))
	e := &manifestEdit{dir: dir, name: name, path: filepath.Join(dir, filepath.FromSlash(name)), nl: "\n"}
	// A symlinked manifest is edited where it points: the link stays a
	// link.
	var err error
	if e.path, err = filepath.EvalSymlinks(e.path); err != nil {
		return nil, err
	}
	if e.raw, err = os.ReadFile(e.path); err != nil {
		return nil, err
	}
	if err := refuseOddBreaks(e.raw); err != nil {
		return nil, fmt.Errorf("%s: %w", e.path, err)
	}
	if bytes.Contains(e.raw, []byte("\r\n")) {
		e.nl = "\r\n"
	}
	if e.m, err = project.ReadManifest(os.DirFS(dir), name); err != nil {
		return nil, fmt.Errorf("%w\n(fix that first: a command edits only a manifest that loads)", err)
	}
	e.orig = e.raw
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
//
// took says whether the loaded result holds what the edit meant to add:
// text can load and still mean something else (a key YAML reads as null,
// lines after a document end marker), and then nothing is written.
func (e *manifestEdit) save(took func(*project.Manifest) bool) (*project.Manifest, error) {
	m, err := e.check()
	if err != nil {
		return nil, fmt.Errorf("the edit would not load, so %s is unchanged: %w", e.path, err)
	}
	if !took(m) {
		return nil, fmt.Errorf("the edit loads but does not say exactly what was meant (a name YAML reads otherwise, or text the file ends before?), so %s is unchanged", e.path)
	}
	st, err := os.Stat(e.path)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(e.path), ".nautilus-edit-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // gone after the rename; cleans up a failure
	_, err = tmp.Write(e.raw)
	if err == nil {
		err = tmp.Chmod(st.Mode().Perm())
	}
	if err == nil {
		err = keepOwner(tmp, st) // run under sudo, the file stays its owner's
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	// No lock: a change made since the read (an editor's save) is not
	// overwritten; the command is run again.
	if err == nil {
		if now, rerr := os.ReadFile(e.path); rerr != nil || !bytes.Equal(now, e.orig) {
			err = fmt.Errorf("%s changed while the command ran, so it is left as it is now: run the command again", e.path)
		}
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
		return &memFile{Reader: bytes.NewReader(o.data), name: path.Base(name), size: int64(len(o.data))}, nil
	}
	return o.base.Open(name)
}

// memFile is the overlay's one file, read from memory.
type memFile struct {
	*bytes.Reader
	name string
	size int64
}

func (f *memFile) Stat() (iofs.FileInfo, error) { return f, nil }
func (f *memFile) Close() error                 { return nil }
func (f *memFile) Name() string                 { return f.name }
func (f *memFile) Size() int64                  { return f.size }
func (f *memFile) Mode() iofs.FileMode          { return 0o644 }
func (f *memFile) ModTime() time.Time           { return time.Time{} }
func (f *memFile) IsDir() bool                  { return false }
func (f *memFile) Sys() any                     { return nil }

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
