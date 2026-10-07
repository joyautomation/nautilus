package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const module = "github.com/joyautomation/nautilus"

// Status is a test's outcome merged across every CI job that ran it.
type Status string

const (
	Pass Status = "pass"
	Fail Status = "fail"
	Skip Status = "skip"
)

// rank orders merged outcomes: a failure in any job wins, then a pass
// (the TCK job passes what the plain `go test ./...` job skips).
func rank(s Status) int {
	switch s {
	case Fail:
		return 3
	case Pass:
		return 2
	case Skip:
		return 1
	}
	return 0
}

func merge(a, b Status) Status {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// Results holds every test outcome from the evidence files.
type Results struct {
	Go   map[string]map[string]Status // package dir ("./modbus") → test path → status
	Pkg  map[string]Status            // package dir → package outcome
	Naut map[string]map[string]Status // project root → test name → status
}

func newResults() *Results {
	return &Results{Go: map[string]map[string]Status{}, Pkg: map[string]Status{}, Naut: map[string]map[string]Status{}}
}

// pkgDir turns an import path into the ./dir form claims use.
func pkgDir(importPath string) string {
	if importPath == module {
		return "."
	}
	return "./" + strings.TrimPrefix(importPath, module+"/")
}

type goEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// readGoJSON reads one `go test -json` stream.
func (r *Results) readGoJSON(rd io.Reader) error {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e goEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		var st Status
		switch e.Action {
		case "pass":
			st = Pass
		case "fail":
			st = Fail
		case "skip":
			st = Skip
		default:
			continue
		}
		if e.Package == "" {
			continue
		}
		dir := pkgDir(e.Package)
		if e.Test == "" {
			r.Pkg[dir] = merge(r.Pkg[dir], st)
			continue
		}
		if r.Go[dir] == nil {
			r.Go[dir] = map[string]Status{}
		}
		r.Go[dir][e.Test] = merge(r.Go[dir][e.Test], st)
	}
	return sc.Err()
}

type nautEvent struct {
	Root   string `json:"root"`
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

// readNautJSON reads `naut test -json` lines that CI tagged with the
// project root ({"root": "examples/lift-station", ...}).
func (r *Results) readNautJSON(rd io.Reader) error {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e nautEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		if e.Root == "" || e.Name == "" {
			continue
		}
		root := filepath.ToSlash(filepath.Clean(e.Root))
		if r.Naut[root] == nil {
			r.Naut[root] = map[string]Status{}
		}
		st := Fail
		if e.Passed {
			st = Pass
		}
		r.Naut[root][e.Name] = merge(r.Naut[root][e.Name], st)
	}
	return sc.Err()
}

// readDir reads every evidence file under dir: *.ndjson whose first event
// carries "root" is naut output, everything else ending .json/.ndjson is
// go test -json.
func (r *Results) readDir(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".json") && !strings.HasSuffix(p, ".ndjson") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		if strings.HasPrefix(filepath.Base(p), "naut") {
			err = r.readNautJSON(f)
		} else {
			err = r.readGoJSON(f)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		return nil
	})
}

// RefResult is how one reference fared.
type RefResult struct {
	Ref     string `json:"ref"`
	Status  string `json:"status"` // pass, fail, skip, missing
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"`
}

// lookup resolves a reference against the results: the test itself and
// every subtest beneath it. A reference to a subtest of a skipped test
// (a hardware-gated test whose subtests never ran) reports skip.
func (r *Results) lookup(ref Ref) RefResult {
	out := RefResult{Ref: ref.Raw, Status: "missing"}
	count := func(s Status) {
		switch s {
		case Pass:
			out.Passed++
		case Fail:
			out.Failed++
		case Skip:
			out.Skipped++
		}
	}
	if ref.Kind == "naut" {
		if s, ok := r.Naut[ref.Dir][ref.Test]; ok {
			count(s)
		}
	} else {
		name := ref.GoName()
		tests := r.Go[ref.Dir]
		for t, s := range tests {
			if t == name || strings.HasPrefix(t, name+"/") {
				count(s)
			}
		}
		if out.Passed+out.Failed+out.Skipped == 0 {
			// Walk up: the nearest ancestor that ran decides.
			for p := name; strings.Contains(p, "/"); {
				p = p[:strings.LastIndex(p, "/")]
				if s, ok := tests[p]; ok {
					if s == Skip {
						count(Skip)
					}
					break
				}
			}
		}
	}
	switch {
	case out.Failed > 0:
		out.Status = string(Fail)
	case out.Passed > 0:
		out.Status = string(Pass)
	case out.Skipped > 0:
		out.Status = string(Skip)
	}
	return out
}

// PackageStat is one Go package's test counts and statement coverage.
type PackageStat struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Tests      int    `json:"tests"` // top-level Test funcs that reported
	Subtests   int    `json:"subtests"`
	Passed     int    `json:"passed"` // tests + subtests
	Failed     int    `json:"failed"`
	Skipped    int    `json:"skipped"`
	Statements int    `json:"statements"`
	Covered    int    `json:"covered"`
}

func (r *Results) packages(cov map[string][2]int) []PackageStat {
	seen := map[string]bool{}
	for d := range r.Pkg {
		seen[d] = true
	}
	for d := range r.Go {
		seen[d] = true
	}
	for d := range cov {
		seen[d] = true
	}
	var out []PackageStat
	for d := range seen {
		p := PackageStat{Path: d, Status: string(r.Pkg[d])}
		for t, s := range r.Go[d] {
			if strings.Contains(t, "/") {
				p.Subtests++
			} else {
				p.Tests++
			}
			switch s {
			case Pass:
				p.Passed++
			case Fail:
				p.Failed++
			case Skip:
				p.Skipped++
			}
		}
		c := cov[d]
		p.Statements, p.Covered = c[0], c[1]
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
