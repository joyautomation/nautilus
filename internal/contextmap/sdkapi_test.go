package contextmap

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sdkPackages are the Go packages CONTRIBUTING.md ("Go API") declares
// stable as whole packages. The seam interfaces it also names (retain.Store,
// hist.Sink, alarm.Journal and Notifier) live in packages that are otherwise
// internal, so an SDK program does not import them whole, and neither may
// what we hand to users.
var sdkPackages = map[string]bool{
	"io":         true,
	"runtime":    true,
	"server":     true,
	"acceptance": true,
}

// sdkSources are what a user starts from: the templates naut new scaffolds a
// Go project from, and the examples. Whatever they import is, in practice,
// the API, so they may import only the stable packages.
var sdkSources = []string{"cmd/naut/templates", "examples"}

// quotedImport finds a nautilus package path in a template, where the Go is
// not yet parseable. Only a quoted path counts: `go install
// .../cmd/naut@latest` in a CI template installs the binary, it imports
// nothing.
var quotedImport = regexp.MustCompile(`"` + regexp.QuoteMeta(module) + `/([^"]+)"`)

// TestSDKSourcesImportOnlyTheStableAPI holds the templates and examples to
// the declared Go API: a scaffold or example that reaches for an internal
// package would teach users to depend on it.
func TestSDKSourcesImportOnlyTheStableAPI(t *testing.T) {
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	var bad []string
	for _, dir := range sdkSources {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "node_modules" || (strings.HasPrefix(name, ".") && path != filepath.Join(root, dir)) {
					return filepath.SkipDir
				}
				return nil
			}
			pkgs, err := nautilusImports(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for _, p := range pkgs {
				seen[p] = true
				if !sdkPackages[p] {
					bad = append(bad, rel+" imports "+module+"/"+p)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s: only io, runtime, server and acceptance are stable as whole packages (CONTRIBUTING.md, Go API); if this is a stable seam interface (retain.Store, hist.Sink, alarm.Journal or Notifier), widen this test to allow exactly that", b)
	}
	// A scan that finds nothing passes vacuously; the go-sdk example and
	// the sdk template do import the runtime.
	if !seen["runtime"] {
		t.Errorf("found no import of %s/runtime under %v: the scan is not reading the sources", module, sdkSources)
	}
}

// nautilusImports lists the module-relative packages a file imports: from
// the import block of a .go file, from quoted paths in a .tmpl.
func nautilusImports(path string) ([]string, error) {
	switch {
	case strings.HasSuffix(path, ".go"):
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, s := range f.Imports {
			p, err := strconv.Unquote(s.Path.Value)
			if err != nil {
				return nil, err
			}
			if rest, ok := strings.CutPrefix(p, module+"/"); ok {
				out = append(out, rest)
			}
		}
		return out, nil
	case strings.HasSuffix(path, ".tmpl"):
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, m := range quotedImport.FindAllStringSubmatch(string(b), -1) {
			out = append(out, m[1])
		}
		return out, nil
	}
	return nil, nil
}
