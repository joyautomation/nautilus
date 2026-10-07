// Command evidence joins the runtime claims inventory (docs/claims/) to
// the test results of a CI run, for the docs site's /verified/runtime/.
//
//	go run ./tools/evidence check
//	    Validate every claim file against the source tree: schema, unique
//	    ids, and that each referenced test function, conformance feature and
//	    acceptance test exists. Runs in CI on every push.
//
//	go test -json ./... | go run ./tools/evidence cat
//	    Print a `go test -json` stream as the plain output it carries, and
//	    exit 1 when any test or package failed. CI tees the JSON to a file
//	    so the logs read as usual while the evidence is kept.
//
//	go run ./tools/evidence build -in evidence/ -cover cover.out -o evidence.json
//	    Merge every job's results (go test -json files, naut-*.ndjson from
//	    `naut test -json` tagged with "root") with the coverage profile and
//	    the claims, and write the evidence file the site renders. -strict
//	    fails when a claim names a test that no job reported.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = runCheck(os.Args[2:])
	case "cat":
		err = runCat(os.Stdin, os.Stdout)
	case "build":
		err = runBuild(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "evidence:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: evidence check | cat | build [flags]  (see go doc ./tools/evidence)")
	os.Exit(2)
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	root := fs.String("root", ".", "repository root")
	fs.Parse(args)
	files, err := loadClaims(filepath.Join(*root, "docs", "claims"))
	if err != nil {
		return err
	}
	errs := checkClaims(*root, files)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, e)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d problem(s) in docs/claims", len(errs))
	}
	n := 0
	for _, f := range files {
		n += len(f.Claims)
	}
	fmt.Printf("docs/claims: %d files, %d claims, every reference resolves\n", len(files), n)
	return nil
}

// runCat prints the Output of each event and reports whether anything
// failed — the same lines `go test` would have printed without -json. A
// line that is not an event (a wrapper script's own output) passes
// through unchanged.
func runCat(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	failed := false
	for sc.Scan() {
		line := sc.Bytes()
		var e struct{ Action, Output string }
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &e) != nil {
			out.Write(append(line, '\n'))
			continue
		}
		io.WriteString(out, e.Output)
		if e.Action == "fail" || e.Action == "build-fail" {
			failed = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("tests failed")
	}
	return nil
}

// Evidence is the file the docs site renders (schema 1).
type Evidence struct {
	Schema      int                `json:"schema"`
	GeneratedAt string             `json:"generatedAt"`
	Sha         string             `json:"sha,omitempty"`
	RunID       string             `json:"runId,omitempty"`
	Totals      Totals             `json:"totals"`
	Packages    []PackageStat      `json:"packages"`
	Conformance []ConformanceRow   `json:"conformance"`
	Pages       []ClaimFile        `json:"pages"`
	Claims      []ClaimResult      `json:"claims"`
	Verdicts    map[string]int     `json:"verdicts"`
	Missing     []string           `json:"missing,omitempty"`
	Naut        map[string]Summary `json:"naut,omitempty"`
}

type Totals struct {
	Packages   int `json:"packages"`
	Tests      int `json:"tests"`
	Subtests   int `json:"subtests"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
	Statements int `json:"statements"`
	Covered    int `json:"covered"`
}

type Summary struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

type ConformanceRow struct {
	Feature string   `json:"feature"`
	Langs   []string `json:"langs"`
	Tests   int      `json:"tests"`
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
}

// ClaimResult is one claim with the verdict of its tests:
//
//	verified  at least one test passed, none failed, not marked partial
//	partial   as verified, but the claim says its tests cover only part
//	failing   a test it names failed
//	unrun     tests named, none ran in CI (hardware-only, or missing)
//	gap       no test names it
type ClaimResult struct {
	ID      string      `json:"id"`
	File    string      `json:"file"`
	Claim   string      `json:"claim"`
	Source  string      `json:"source"`
	Partial bool        `json:"partial,omitempty"`
	Note    string      `json:"note,omitempty"`
	Verdict string      `json:"verdict"`
	Refs    []RefResult `json:"refs"`
}

var languages = []string{"st", "ld", "fbd", "sfc"}

func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	root := fs.String("root", ".", "repository root")
	in := fs.String("in", "evidence", "directory of go test -json files and naut-*.ndjson")
	cover := fs.String("cover", "", "coverage profile (optional)")
	outPath := fs.String("o", "evidence.json", "output file")
	strict := fs.Bool("strict", false, "fail when a claim names a test no job reported")
	fs.Parse(args)

	files, err := loadClaims(filepath.Join(*root, "docs", "claims"))
	if err != nil {
		return err
	}
	if errs := checkClaims(*root, files); len(errs) > 0 {
		return fmt.Errorf("docs/claims does not check (go run ./tools/evidence check):\n%s", strings.Join(errs, "\n"))
	}
	res := newResults()
	if err := res.readDir(*in); err != nil {
		return err
	}
	cov := map[string][2]int{}
	if *cover != "" {
		f, err := os.Open(*cover)
		if err != nil {
			return err
		}
		cov, err = readCover(f)
		f.Close()
		if err != nil {
			return err
		}
	}

	ev := Evidence{
		Schema:      1,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Sha:         os.Getenv("GITHUB_SHA"),
		RunID:       os.Getenv("GITHUB_RUN_ID"),
		Packages:    res.packages(cov),
		Pages:       files,
		Verdicts:    map[string]int{},
	}
	for _, p := range ev.Packages {
		t := &ev.Totals
		t.Packages++
		t.Tests += p.Tests
		t.Subtests += p.Subtests
		t.Passed += p.Passed
		t.Failed += p.Failed
		t.Skipped += p.Skipped
		t.Statements += p.Statements
		t.Covered += p.Covered
	}
	for root, tests := range res.Naut {
		s := Summary{}
		for _, st := range tests {
			if st == Pass {
				s.Passed++
			} else {
				s.Failed++
			}
		}
		if ev.Naut == nil {
			ev.Naut = map[string]Summary{}
		}
		ev.Naut[root] = s
	}
	ev.Conformance, err = conformance(*root, res)
	if err != nil {
		return err
	}

	missing := map[string]bool{}
	for _, f := range files {
		for _, c := range f.Claims {
			cr := ClaimResult{ID: c.ID, File: f.File, Claim: strings.TrimSpace(c.Claim), Source: c.Source, Partial: c.Partial, Note: strings.TrimSpace(c.Note), Refs: []RefResult{}}
			for _, t := range c.Tests {
				ref, _ := parseRef(t) // checked above
				rr := res.lookup(ref)
				if rr.Status == "missing" {
					missing[ref.Raw] = true
				}
				cr.Refs = append(cr.Refs, rr)
			}
			cr.Verdict = verdict(cr)
			ev.Verdicts[cr.Verdict]++
			ev.Claims = append(ev.Claims, cr)
		}
	}
	for m := range missing {
		ev.Missing = append(ev.Missing, m)
	}
	sort.Strings(ev.Missing)

	b, err := json.MarshalIndent(ev, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %d packages, %d tests + %d subtests, %d claims %v\n", *outPath, ev.Totals.Packages, ev.Totals.Tests, ev.Totals.Subtests, len(ev.Claims), ev.Verdicts)
	if len(ev.Missing) > 0 {
		fmt.Fprintf(os.Stderr, "%d reference(s) matched no test result:\n  %s\n", len(ev.Missing), strings.Join(ev.Missing, "\n  "))
		if *strict {
			return fmt.Errorf("claims name tests no job ran (rename? subtest typo?)")
		}
	}
	return nil
}

func verdict(c ClaimResult) string {
	if len(c.Refs) == 0 {
		return "gap"
	}
	passed := false
	for _, r := range c.Refs {
		switch r.Status {
		case string(Fail):
			return "failing"
		case string(Pass):
			passed = true
		}
	}
	switch {
	case !passed:
		return "unrun"
	case c.Partial:
		return "partial"
	}
	return "verified"
}

// conformance lists the corpus features with their languages and how
// their YAML tests fared in this run.
func conformance(root string, res *Results) ([]ConformanceRow, error) {
	dir := filepath.Join(root, "lang", "conformance")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	c := &checker{root: root, funcs: map[string]map[string]bool{}, suite: map[string]map[string]bool{}}
	tests := res.Go["./lang/conformance"]
	var out []ConformanceRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, err := os.Stat(filepath.Join(dir, name, "nautilus.yaml")); err != nil {
			continue
		}
		row := ConformanceRow{Feature: name, Langs: []string{}}
		for _, l := range languages {
			if _, err := os.Stat(filepath.Join(dir, name, name+"."+l)); err == nil {
				row.Langs = append(row.Langs, l)
			}
		}
		row.Tests = len(c.suiteNames(filepath.Join("lang", "conformance", name)))
		prefix := "TestConformance/" + name + "/"
		for t, s := range tests {
			if strings.HasPrefix(t, prefix) && !strings.Contains(t[len(prefix):], "/") {
				switch s {
				case Pass:
					row.Passed++
				case Fail:
					row.Failed++
				}
			}
		}
		out = append(out, row)
	}
	return out, nil
}
