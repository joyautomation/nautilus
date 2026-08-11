package lsp

// Manifest awareness.
//
// The analyzer knows what a PROGRAM declares; nautilus.yaml knows what the
// PROJECT declares — every tag's role, its engineering unit, and the
// sentence describing it. Those are different facts, and the second set is
// the one an author is usually missing: hovering TempC should say "°C,
// tank temperature, driven by the sim task", not just "REAL".
//
// It also answers a question the analyzer cannot: which tags exist at all.
// That is what a test file needs, since its expectations name tags rather
// than a program's locals.
//
// Reading is cheap (a YAML decode, no compile) and cached against the
// manifest's modtime, because this runs on a keystroke.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/internal/project"
)

// ProjectTag is one tag as nautilus.yaml declares it.
type ProjectTag struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Type string `json:"type"` // inferred from init: BOOL / REAL / STRING
	Unit string `json:"unit,omitempty"`
	Desc string `json:"desc,omitempty"`
}

// manifestCache remembers one parse per manifest path, invalidated by
// modtime so an edit to nautilus.yaml is picked up without a restart.
type manifestCache struct {
	mu      sync.Mutex
	entries map[string]*manifestEntry
}

type manifestEntry struct {
	mod  int64
	tags []ProjectTag
}

var manifests = manifestCache{entries: map[string]*manifestEntry{}}

// findManifest walks up from a file looking for nautilus.yaml, stopping at
// the filesystem root. A project is a directory with a manifest in it, so
// this is the same question `nautilus run` answers by being run there.
func findManifest(path string) (string, bool) {
	dir := filepath.Dir(path)
	for range 32 { // bounded: a symlink cycle must not hang the server
		candidate := filepath.Join(dir, project.ManifestName)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	return "", false
}

// projectTags returns the tags declared by the manifest governing path, or
// nil when there is no manifest (a bare .st file outside a project, which
// is a perfectly good thing to edit).
func projectTags(path string) []ProjectTag {
	mpath, ok := findManifest(path)
	if !ok {
		return nil
	}
	st, err := os.Stat(mpath)
	if err != nil {
		return nil
	}
	mod := st.ModTime().UnixNano()

	manifests.mu.Lock()
	defer manifests.mu.Unlock()
	prev, hadPrev := manifests.entries[mpath]
	if hadPrev && prev.mod == mod {
		return prev.tags
	}
	tags, err := readTags(mpath)
	if err != nil {
		// A manifest being edited is invalid for most of the keystrokes it
		// takes to add a tag. Serving the last good answer beats completion
		// blinking out mid-edit — and the failure is deliberately NOT
		// cached, so the next keystroke retries.
		if hadPrev {
			return prev.tags
		}
		return nil
	}
	manifests.entries[mpath] = &manifestEntry{mod: mod, tags: tags}
	return tags
}

func readTags(mpath string) ([]ProjectTag, error) {
	m, err := project.ReadManifest(os.DirFS(filepath.Dir(mpath)), "")
	if err != nil {
		return nil, err
	}
	out := make([]ProjectTag, 0, len(m.Tags))
	for _, t := range m.Tags {
		if t.Name == "" {
			continue
		}
		// A declared type is the truth; inferring from the seed is the
		// fallback that existed only because there was nothing better. This
		// also answers the type of a tag with no seed at all, which
		// inference never could.
		typ := t.Type
		if typ == "" {
			typ = iecTypeOf(t.Init)
		}
		out = append(out, ProjectTag{
			Name: t.Name,
			Role: strings.ToLower(t.Role),
			Type: typ,
			Unit: t.Unit,
			Desc: t.Desc,
		})
	}
	return out, nil
}

// iecTypeOf infers a tag's IEC type from its declared initial value. The
// manifest does not state types — the seed's shape is the declaration, the
// same way the runtime's tag store derives a kind from what it is given.
// A role:input tag may have no init, in which case the type is unknown and
// saying nothing beats guessing.
func iecTypeOf(init any) string {
	switch init.(type) {
	case bool:
		return "BOOL"
	case string:
		return "STRING"
	case int, int64, float64:
		return "REAL"
	}
	return ""
}

// hoverDoc renders a tag's manifest facts as markdown, for hover and for a
// completion item's documentation.
func (t ProjectTag) hoverDoc() string {
	var b strings.Builder
	b.WriteString("```iec-st\n")
	b.WriteString(t.Name)
	if t.Type != "" {
		b.WriteString(" : " + t.Type)
	}
	b.WriteString("\n```")
	if t.Desc != "" {
		b.WriteString("\n\n" + t.Desc)
	}
	var facts []string
	if t.Role != "" {
		facts = append(facts, roleBlurb(t.Role))
	}
	if t.Unit != "" {
		facts = append(facts, "measured in "+t.Unit)
	}
	if len(facts) > 0 {
		b.WriteString("\n\n*" + strings.Join(facts, " — ") + "*")
	}
	return b.String()
}

// roleBlurb says what a role MEANS in the scan, which is the part that
// actually bites: writing to an input is pointless because the driver
// overwrites it before every scan.
func roleBlurb(role string) string {
	switch role {
	case "input":
		return "input — the driver writes it before each scan"
	case "output":
		return "output — the logic writes it, the driver reads it after each scan"
	case "setpoint":
		return "setpoint — seeded, operator-writable"
	case "state":
		return "state — logic-owned, retained across scans"
	}
	return role
}

// detail is the one-line summary shown beside a completion item.
func (t ProjectTag) detail() string {
	parts := []string{}
	if t.Type != "" {
		parts = append(parts, t.Type)
	}
	if t.Role != "" {
		parts = append(parts, t.Role)
	}
	if t.Unit != "" {
		parts = append(parts, t.Unit)
	}
	return strings.Join(parts, " · ")
}

// manifestTag looks up one tag by name for the project governing uri.
func (s *Server) manifestTag(uri, name string) (ProjectTag, bool) {
	for _, t := range s.projectTagsFor(uri) {
		if strings.EqualFold(t.Name, name) {
			return t, true
		}
	}
	return ProjectTag{}, false
}

// projectTagsFor resolves a document URI to its project's tags. Documents
// that are not files (untitled:, vscode-vfs:) have no project.
func (s *Server) projectTagsFor(uri string) []ProjectTag {
	path, ok := uriToPath(uri)
	if !ok {
		return nil
	}
	return projectTags(path)
}

// inVarExternal reports whether line (1-based) falls inside a VAR_EXTERNAL
// block. A text scan rather than an analysis lookup, because completion is
// asked mid-keystroke when the buffer usually does not parse.
func inVarExternal(text string, line int) bool {
	inBlock := false
	for i, l := range strings.Split(text, "\n") {
		if i+1 > line {
			break
		}
		switch upper := strings.ToUpper(strings.TrimSpace(l)); {
		case strings.HasPrefix(upper, "VAR_EXTERNAL"):
			inBlock = true
		case strings.HasPrefix(upper, "END_VAR"):
			inBlock = false
		case strings.HasPrefix(upper, "VAR"):
			// VAR / VAR_INPUT / VAR_OUTPUT / VAR_TEMP open a different block.
			inBlock = false
		}
	}
	return inBlock
}
