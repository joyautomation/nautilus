package lsp

// nautilus/descriptions: every name a diagram might draw → the sentence
// that describes it, so the ladder, FBD and SFC editors can show a tag's
// description on the element (Studio 5000 draws it above the instruction;
// #216). One request per model post answers the whole file, from the two
// places a description lives:
//
//   - the manifest: tags[].desc in nautilus.yaml and its tag-files — the
//     same cached read (projectTags, keyed on the manifest's modtime) that
//     hover and completion use, so a tag means the same thing in a tooltip
//     on the diagram as under the mouse in the text;
//   - the file itself: a trailing comment on a VAR declaration line,
//     `LeadFailed : BOOL; (* the lead pump's FailToRun *)` — the IEC habit
//     for documenting a local, and the only description a local has.
//
// The file's own comment wins over the manifest: it is the more specific
// of the two, written where the name is used. Names match case-insensitively
// (IEC identifiers are), and the answer keeps the spelling that won.

import (
	"os"
	"regexp"
	"strings"
)

// DescriptionsParams is the nautilus/descriptions request.
type DescriptionsParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// DescriptionsResult maps a name (as declared) to its description.
type DescriptionsResult struct {
	Descriptions map[string]string `json:"descriptions"`
}

func (s *Server) handleDescriptions(m *message) {
	var p DescriptionsParams
	if !unmarshal(m.Params, &p) {
		s.w.respondError(m.ID, codeInvalidParams, "bad descriptions params")
		return
	}
	uri := p.TextDocument.URI
	text := ""
	if doc, ok := s.docs[uri]; ok {
		text = doc.text // an unsaved buffer wins over the disk
	} else if path, ok := uriToPath(uri); ok {
		if b, err := os.ReadFile(path); err == nil {
			text = string(b)
		}
	}
	s.w.respond(m.ID, DescriptionsResult{Descriptions: descriptionsFor(s.projectTagsFor(uri), text)})
}

// descriptionsFor merges the manifest's tag descriptions with the file's
// own VAR comments; the comment wins a case-insensitive tie.
func descriptionsFor(tags []ProjectTag, text string) map[string]string {
	out := map[string]string{}
	byFold := map[string]string{} // lower-cased name → key in out
	put := func(name, desc string) {
		if prev, ok := byFold[strings.ToLower(name)]; ok {
			delete(out, prev)
		}
		byFold[strings.ToLower(name)] = name
		out[name] = desc
	}
	for _, t := range tags {
		if d := strings.TrimSpace(t.Desc); d != "" {
			put(t.Name, d)
		}
	}
	for _, c := range VarComments(text) {
		put(c.Name, c.Desc)
	}
	return out
}

// VarComment is one declaration's trailing comment.
type VarComment struct {
	Name string
	Desc string
	Line int // 1-based
}

var (
	varOpenRe  = regexp.MustCompile(`(?i)^\s*VAR(_[A-Z_]+)?\b`)
	varCloseRe = regexp.MustCompile(`(?i)^\s*END_VAR\b`)
	// names : type-and-init ; comment — the comment is (* … *) or // ….
	// `AT %IX0.0` between the names and the colon is allowed.
	varDeclRe = regexp.MustCompile(`^\s*([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)\s*(?:(?i:AT)\s+%\S+\s*)?:[^;]*;\s*(?:\(\*\s*(.*?)\s*\*\)|//\s*(.*?))\s*$`)
)

// VarComments returns the trailing comment of every declaration inside a
// VAR … END_VAR block (any VAR_ section) that has one. A declaration that
// names several variables gives each the same comment.
func VarComments(text string) []VarComment {
	var out []VarComment
	in := false
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case !in && varOpenRe.MatchString(line):
			in = true
			continue
		case in && varCloseRe.MatchString(line):
			in = false
			continue
		case !in:
			continue
		}
		m := varDeclRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		desc := strings.TrimSpace(m[2] + m[3])
		if desc == "" || strings.HasPrefix(desc, "@") { // (* @layout … *)-style pragmas are not prose
			continue
		}
		for _, name := range strings.Split(m[1], ",") {
			out = append(out, VarComment{Name: strings.TrimSpace(name), Desc: desc, Line: i + 1})
		}
	}
	return out
}
