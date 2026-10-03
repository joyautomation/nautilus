// Command conformance prints the language-conformance corpus as a
// feature × language matrix:
//
//	go run ./lang/conformance -matrix
//
// The corpus itself is the set of feature directories beside this file;
// `go test ./lang/conformance/` runs every one of them (conformance_test.go).
// See README.md for how a feature is laid out and how to add one.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/acceptance"
)

// Languages is the column order of the matrix, and the file extensions a
// feature directory may carry: `<feature>.st`, `<feature>.ld`, …
var Languages = []string{"st", "ld", "fbd", "sfc"}

// Feature is one directory of the corpus: a manifest project plus its
// acceptance suite.
type Feature struct {
	Name  string          // the directory's base name, e.g. "fb-ton"
	Dir   string          // path on disk
	Langs map[string]bool // language → `<Name>.<lang>` exists
	Tests int             // tests across every *_test.yaml in the directory
}

// Features lists every feature under root (a directory holding
// nautilus.yaml), sorted by name. A directory without a manifest is not a
// feature and is skipped; README.md and the Go sources live at root itself.
func Features(root string) ([]Feature, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Feature
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "nautilus.yaml")); err != nil {
			continue
		}
		f := Feature{Name: e.Name(), Dir: dir, Langs: map[string]bool{}}
		for _, lang := range Languages {
			if _, err := os.Stat(filepath.Join(dir, f.Name+"."+lang)); err == nil {
				f.Langs[lang] = true
			}
		}
		fsys := os.DirFS(dir)
		suites, err := acceptance.DiscoverSuites(fsys)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		for _, p := range suites {
			s, err := acceptance.LoadSuite(fsys, p)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			f.Tests += len(s.Tests)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LanguageFiles returns every `<Name>.<lang>` present in the feature's
// directory, in matrix column order — the files the walker insists are
// wired to a task.
func (f Feature) LanguageFiles() []string {
	var out []string
	for _, lang := range Languages {
		if f.Langs[lang] {
			out = append(out, f.Name+"."+lang)
		}
	}
	return out
}

// Matrix renders the markdown table: one row per feature, ✓ where the
// language file exists, and the test count.
func Matrix(feats []Feature) string {
	var b strings.Builder
	b.WriteString("| Feature |")
	for _, l := range Languages {
		fmt.Fprintf(&b, " %s |", strings.ToUpper(l))
	}
	b.WriteString(" Tests |\n|---|")
	for range Languages {
		b.WriteString(":-:|")
	}
	b.WriteString("--:|\n")
	total := 0
	for _, f := range feats {
		fmt.Fprintf(&b, "| `%s` |", f.Name)
		for _, l := range Languages {
			mark := " "
			if f.Langs[l] {
				mark = "✓"
			}
			fmt.Fprintf(&b, " %s |", mark)
		}
		fmt.Fprintf(&b, " %d |\n", f.Tests)
		total += f.Tests
	}
	fmt.Fprintf(&b, "\n%d features, %d tests.\n", len(feats), total)
	return b.String()
}

// corpusRoot finds the corpus from wherever the command was run: the
// repo root (`go run ./lang/conformance`) or the directory itself.
func corpusRoot() string {
	for _, cand := range []string{"lang/conformance", "."} {
		if _, err := fs.Stat(os.DirFS(cand), "README.md"); err == nil {
			return cand
		}
	}
	return "."
}

func main() {
	matrix := flag.Bool("matrix", false, "print the feature × language table as markdown")
	root := flag.String("dir", "", "corpus directory (default: lang/conformance under the cwd, else the cwd)")
	flag.Parse()
	if !*matrix {
		fmt.Fprintln(os.Stderr, "usage: go run ./lang/conformance -matrix [-dir lang/conformance]")
		os.Exit(2)
	}
	if *root == "" {
		*root = corpusRoot()
	}
	feats, err := Features(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "conformance:", err)
		os.Exit(1)
	}
	fmt.Print(Matrix(feats))
}
