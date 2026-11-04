package lsp

import (
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/lang/lint"
	"github.com/joyautomation/nautilus/lang/st"
)

// The diagram structure warnings (lang/lint, nautilus#561) on the
// keystroke: the same rules `naut check` runs, with the same IDs (as the
// diagnostic's code) and the governing manifest's check: settings. The
// ladder, FBD and SFC editors show a document's diagnostics on the rung,
// network or step they name, so a warning here reaches the diagram too.

const structureSource = "nautilus (structure)"

// structureDiagnostics lints one buffer. The caller runs it only on a
// buffer that compiles: structure advice on a broken file is noise.
func structureDiagnostics(path, text string) []Diagnostic {
	cfg := lint.Defaults()
	if e := manifestFor(path); e != nil && e.structure != nil {
		cfg = *e.structure
	}
	fs := project.Structure(path, text, cfg)
	if len(fs) == 0 {
		return nil
	}
	out := make([]Diagnostic, 0, len(fs))
	for _, f := range fs {
		r := lineRange(text, f.Line)
		if f.Col > 1 {
			r = posRange(text, st.Pos{Line: f.Line, Col: f.Col})
		}
		out = append(out, Diagnostic{
			Range:    r,
			Severity: SeverityWarning,
			Code:     f.Rule,
			Source:   structureSource,
			Message:  f.Message,
		})
	}
	return out
}
