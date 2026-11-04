package project

import (
	"path/filepath"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/lint"
	"github.com/joyautomation/nautilus/lang/sfc"
)

// Structure is the diagram structure warnings (lang/lint, nautilus#561) for
// one source file, by its language: ladder, FBD and SFC have rules; ST and
// anything else has none. `naut check` and the language server both call
// this, so the terminal and the editor say the same thing.
func Structure(path, src string, cfg lint.Config) []lint.Finding {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ld":
		return ld.Lint(src, cfg)
	case ".fbd":
		return fbd.Lint(src, cfg)
	case ".sfc":
		return sfc.Lint(src, cfg)
	}
	return nil
}
