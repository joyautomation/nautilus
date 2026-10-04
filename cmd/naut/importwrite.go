package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// importFile is one file an importer writes.
type importFile struct {
	path string
	body []byte
	// generated: rewritten whatever is there (hw_types.st: every importer
	// writes the same contract set, and it says "do not edit").
	generated bool
}

// writeImport writes an import's files, refusing — before writing any —
// to replace a manifest or tag file that holds something else: a hand
// edit, or another device's import into the same directory. The same
// bytes (a re-import of the same device) are fine; --force replaces.
func writeImport(files []importFile, force bool) error {
	if !force {
		var differ []string
		for _, f := range files {
			if f.generated {
				continue
			}
			old, err := os.ReadFile(f.path)
			if err == nil && !bytes.Equal(old, f.body) {
				differ = append(differ, f.path)
			}
		}
		if len(differ) > 0 {
			what := "it holds"
			if len(differ) > 1 {
				what = "they hold"
			}
			return fmt.Errorf("would replace %s: %s something else (a hand edit, or another device's import). Pass --force to replace, or --out to import this device into its own directory", strings.Join(differ, ", "), what)
		}
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.path, f.body, 0o644); err != nil {
			return err
		}
	}
	return nil
}
