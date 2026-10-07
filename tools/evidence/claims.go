package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/acceptance"
)

// ClaimFile is one docs/claims/<page>.yaml: the claims one documentation
// page makes. See docs/claims/README.md for the schema.
type ClaimFile struct {
	Page   string  `yaml:"page" json:"page"`
	Title  string  `yaml:"title" json:"title"`
	Prefix string  `yaml:"prefix" json:"prefix"`
	Claims []Claim `yaml:"claims" json:"-"`

	File string `yaml:"-" json:"file"` // base name, e.g. "modbus.yaml"
}

// Claim is one behavioural promise and the tests that prove it.
type Claim struct {
	ID      string   `yaml:"id"`
	Claim   string   `yaml:"claim"`
	Source  string   `yaml:"source"`
	Tests   []string `yaml:"tests"`
	Partial bool     `yaml:"partial"`
	Note    string   `yaml:"note"`
}

// Ref is one parsed test reference:
//
//	go <package dir> <TestName>[/<subtest>...]
//	naut <project root> <test name>
type Ref struct {
	Kind string // "go" or "naut"
	Dir  string // package dir ("./modbus") or project root ("examples/lift-station")
	Test string // test path as written
	Raw  string
}

func parseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	kind, rest, ok := strings.Cut(s, " ")
	if !ok || (kind != "go" && kind != "naut") {
		return Ref{}, fmt.Errorf("%q: a test reference starts with `go ` or `naut `", s)
	}
	dir, test, ok := strings.Cut(strings.TrimSpace(rest), " ")
	test = strings.TrimSpace(test)
	if !ok || test == "" {
		return Ref{}, fmt.Errorf("%q: expected `%s <dir> <test>`", s, kind)
	}
	if kind == "go" && !strings.HasPrefix(dir, "./") {
		return Ref{}, fmt.Errorf("%q: a go package dir starts with ./", s)
	}
	return Ref{Kind: kind, Dir: dir, Test: test, Raw: s}, nil
}

// GoName is the test path as `go test -json` reports it: go rewrites the
// spaces in a subtest name to underscores.
func (r Ref) GoName() string { return strings.ReplaceAll(r.Test, " ", "_") }

// loadClaims reads every *.yaml under dir, sorted by file name. A file
// that does not parse is left out and reported in problems, so one broken
// file never hides the rest; err is only for an unreadable directory.
func loadClaims(dir string) (files []ClaimFile, problems []string, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(paths)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		var f ClaimFile
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&f); err != nil {
			problems = append(problems, fmt.Sprintf("docs/claims/%s: %v", filepath.Base(p), err))
			continue
		}
		f.File = filepath.Base(p)
		files = append(files, f)
	}
	return files, problems, nil
}

var testFunc = regexp.MustCompile(`(?m)^func (Test\w+)\(`)

// checker validates claim files against the source tree rooted at root:
// everything that can be known without running a test.
type checker struct {
	root  string
	funcs map[string]map[string]bool // package dir → declared Test funcs
	suite map[string]map[string]bool // project dir → acceptance test names
	errs  []string
}

func (c *checker) errorf(format string, a ...any) { c.errs = append(c.errs, fmt.Sprintf(format, a...)) }

func (c *checker) testFuncs(dir string) map[string]bool {
	if m, ok := c.funcs[dir]; ok {
		return m
	}
	m := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(c.root, dir, "*_test.go"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, sm := range testFunc.FindAllSubmatch(b, -1) {
			m[string(sm[1])] = true
		}
	}
	c.funcs[dir] = m
	return m
}

// suiteNames lists the acceptance test names in every *_test.yaml of a
// project directory; nil when the directory has none.
func (c *checker) suiteNames(dir string) map[string]bool {
	if m, ok := c.suite[dir]; ok {
		return m
	}
	var m map[string]bool
	fsys := os.DirFS(filepath.Join(c.root, dir))
	if paths, err := acceptance.DiscoverSuites(fsys); err == nil && len(paths) > 0 {
		m = map[string]bool{}
		for _, p := range paths {
			s, err := acceptance.LoadSuite(fsys, p)
			if err != nil {
				c.errorf("%s/%s: %v", dir, p, err)
				continue
			}
			for _, t := range s.Tests {
				m[t.Name] = true
			}
		}
	}
	c.suite[dir] = m
	return m
}

var idPattern = regexp.MustCompile(`^([A-Z0-9]+)-\d{3}$`)

func (c *checker) check(files []ClaimFile) {
	prefixes := map[string]string{}
	ids := map[string]string{}
	for _, f := range files {
		where := "docs/claims/" + f.File
		if f.Page == "" {
			c.errorf("%s: page is required", where)
		} else if _, err := os.Stat(filepath.Join(c.root, f.Page)); err != nil {
			c.errorf("%s: page %s does not exist", where, f.Page)
		}
		if f.Title == "" {
			c.errorf("%s: title is required", where)
		}
		if f.Prefix == "" {
			c.errorf("%s: prefix is required", where)
		} else if other, dup := prefixes[f.Prefix]; dup {
			c.errorf("%s: prefix %s is also used by %s", where, f.Prefix, other)
		} else {
			prefixes[f.Prefix] = f.File
		}
		if len(f.Claims) == 0 {
			c.errorf("%s: no claims", where)
		}
		for _, cl := range f.Claims {
			at := where + " " + cl.ID
			m := idPattern.FindStringSubmatch(cl.ID)
			switch {
			case m == nil:
				c.errorf("%s: id must look like %s-001", at, f.Prefix)
			case m[1] != f.Prefix:
				c.errorf("%s: id prefix is not the file's prefix %s", at, f.Prefix)
			}
			if other, dup := ids[cl.ID]; dup {
				c.errorf("%s: id also used in %s", at, other)
			}
			ids[cl.ID] = f.File
			if strings.TrimSpace(cl.Claim) == "" {
				c.errorf("%s: claim is empty", at)
			}
			if strings.TrimSpace(cl.Source) == "" {
				c.errorf("%s: source is empty", at)
			}
			for _, t := range cl.Tests {
				r, err := parseRef(t)
				if err != nil {
					c.errorf("%s: %v", at, err)
					continue
				}
				c.checkRef(at, r)
			}
		}
	}
}

func (c *checker) checkRef(at string, r Ref) {
	if st, err := os.Stat(filepath.Join(c.root, r.Dir)); err != nil || !st.IsDir() {
		c.errorf("%s: %s: no directory %s", at, r.Raw, r.Dir)
		return
	}
	if r.Kind == "naut" {
		names := c.suiteNames(r.Dir)
		if names == nil {
			c.errorf("%s: %s: %s has no *_test.yaml", at, r.Raw, r.Dir)
		} else if !names[r.Test] {
			c.errorf("%s: %s: no test named %q in %s", at, r.Raw, r.Test, r.Dir)
		}
		return
	}
	top, sub, _ := strings.Cut(r.Test, "/")
	if !c.testFuncs(r.Dir)[top] {
		c.errorf("%s: %s: no func %s in %s/*_test.go", at, r.Raw, top, r.Dir)
		return
	}
	// The conformance corpus's subtests are its feature directories and
	// their YAML tests, so they can be checked without running anything.
	// Other subtests are checked against the test events at build time.
	if r.Dir == "./lang/conformance" && top == "TestConformance" && sub != "" {
		feature, name, _ := strings.Cut(sub, "/")
		dir := filepath.Join(r.Dir, feature)
		if _, err := os.Stat(filepath.Join(c.root, dir, "nautilus.yaml")); err != nil {
			c.errorf("%s: %s: no conformance feature %s", at, r.Raw, feature)
			return
		}
		if name != "" && !c.suiteNames(dir)[name] {
			c.errorf("%s: %s: no test named %q in %s", at, r.Raw, name, dir)
		}
	}
}

func checkClaims(root string, files []ClaimFile) []string {
	c := &checker{root: root, funcs: map[string]map[string]bool{}, suite: map[string]map[string]bool{}}
	c.check(files)
	return c.errs
}
