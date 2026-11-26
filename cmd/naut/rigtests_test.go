package main

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// rigRoots start a real pair member (a quorum elector, its witness link,
// the peer listeners): a test that reaches one runs real elections on
// loopback for several leases. So does every function of this package's
// tests that returns one of rigTypes (newBoxRig, newPairRig,
// startPairNode, ...).
var (
	rigRoots = []string{"newPairMember"}
	rigTypes = map[string]bool{"boxRig": true, "pairRig": true, "pairNode": true}
)

// Every cmd/naut test that starts a real pair member is on
// .github/scripts/naut-rig-tests.txt, so it runs in the rig job and not
// beside every other package in the main Go job, where a starved runner
// stalls its elections (#1120): 19 such tests had been left off the list
// by hand. A test reaches a member through any chain of this package's
// plain test helpers. Only plain calls (and functions passed as values)
// are followed, not method calls: a rig's methods are reached only
// through the constructor that made the rig, and following x.start()
// by name would take in every other type's start.
func TestRigTestsAreListed(t *testing.T) {
	listed := map[string]bool{}
	f, err := os.Open(filepath.Join("..", "..", ".github", "scripts", "naut-rig-tests.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			listed[l] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	missing := unlistedRigTests(t, ".", listed)
	if len(missing) > 0 {
		t.Errorf("%d cmd/naut tests start a real pair member but are not on .github/scripts/naut-rig-tests.txt (add them, one per line):\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// unlistedRigTests parses dir's _test.go files and returns, sorted, the
// Test functions that reach a rigRoots call and are not in listed.
func unlistedRigTests(t *testing.T, dir string, listed map[string]bool) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	// calls: function or method name -> the names its body calls. Methods
	// and functions share one name space (a call x.f() is "f").
	calls := map[string]map[string]bool{}
	var tests []string
	fset := token.NewFileSet()
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if fd.Recv == nil && strings.HasPrefix(name, "Test") && name != "TestMain" {
				tests = append(tests, name)
			}
			set := calls[name]
			if set == nil {
				set = map[string]bool{}
				calls[name] = set
			}
			if fd.Recv != nil {
				continue // methods: see TestRigTestsAreListed
			}
			if fd.Type.Results != nil {
				ast.Inspect(fd.Type.Results, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && rigTypes[id.Name] {
						set[rigRoots[0]] = true // it hands out a rig
					}
					return true
				})
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					ast.Inspect(x.X, func(m ast.Node) bool { // r.nodes[f()]: the f is a plain call
						if c, ok := m.(*ast.CallExpr); ok {
							if id, ok := c.Fun.(*ast.Ident); ok {
								set[id.Name] = true
							}
						}
						return true
					})
					return false // x.Sel is a method or field, not followed
				case *ast.Ident:
					// A plain call, or a helper passed as a value.
					set[x.Name] = true
				}
				return true
			})
		}
	}
	reaches := map[string]bool{}
	for _, r := range rigRoots {
		reaches[r] = true
	}
	for changed := true; changed; {
		changed = false
		for fn, set := range calls {
			if reaches[fn] {
				continue
			}
			for c := range set {
				if reaches[c] {
					reaches[fn], changed = true, true
					break
				}
			}
		}
	}
	var missing []string
	for _, name := range tests {
		if reaches[name] && !listed[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
