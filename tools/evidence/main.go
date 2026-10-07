// Command evidence joins the runtime claims inventory (docs/claims/) to
// the test results of a CI run, for the docs site's /verified/runtime/.
//
//	go run ./tools/evidence check
//	    Validate every claim file against the source tree: schema, unique
//	    ids, and that each referenced test function, conformance feature and
//	    acceptance test exists. Exits 1 on any problem. CI never gates a
//	    PR on it: the runtime-evidence job reports it instead.
//
//	go test -json ./... | go run ./tools/evidence cat
//	    Print a `go test -json` stream as the plain output it carries, and
//	    exit 1 when any test or package failed. CI tees the JSON to a file
//	    so the logs read as usual while the evidence is kept.
//
//	go run ./tools/evidence build -in evidence/ -cover cover.out -o evidence.json
//	    Merge every job's results (go test -json files, naut-*.ndjson from
//	    `naut test -json` tagged with "root") with the coverage profile and
//	    the claims, and write the evidence file the site renders. Claim
//	    problems are recorded, not fatal: a broken file is left out, a
//	    broken reference reads "not reported". -strict exits 1 when there is
//	    any problem or a reference no job reported (CI uses it on main to
//	    drive the rolling issue). -baseline <evidence.json> compares with an
//	    earlier run and -summary <file> writes the comparison as markdown
//	    (CI's PR job summary).
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
	files, problems, err := loadClaims(filepath.Join(*root, "docs", "claims"))
	if err != nil {
		return err
	}
	errs := append(problems, checkClaims(*root, files)...)
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
	Problems    []string           `json:"problems,omitempty"` // claim files/refs that do not check
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
	strict := fs.Bool("strict", false, "exit 1 on any claim problem or a reference no job reported")
	baseline := fs.String("baseline", "", "an earlier evidence.json to compare with (optional)")
	summary := fs.String("summary", "", "write the run's claims summary as markdown here (optional)")
	fs.Parse(args)

	files, problems, err := loadClaims(filepath.Join(*root, "docs", "claims"))
	if err != nil {
		return err
	}
	problems = append(problems, checkClaims(*root, files)...)
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
		Problems:    problems,
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
				ref, err := parseRef(t)
				if err != nil { // reported in problems
					cr.Refs = append(cr.Refs, RefResult{Ref: strings.TrimSpace(t), Status: "missing"})
					continue
				}
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
	for _, p := range ev.Problems {
		fmt.Fprintln(os.Stderr, "problem:", p)
	}
	if len(ev.Missing) > 0 {
		fmt.Fprintf(os.Stderr, "%d reference(s) matched no test result:\n  %s\n", len(ev.Missing), strings.Join(ev.Missing, "\n  "))
	}
	if *summary != "" {
		var base *Evidence
		if *baseline != "" {
			if b, err := os.ReadFile(*baseline); err == nil {
				base = &Evidence{}
				if json.Unmarshal(b, base) != nil {
					base = nil
				}
			}
		}
		if err := os.WriteFile(*summary, []byte(summarize(&ev, base)), 0o644); err != nil {
			return err
		}
	}
	if *strict && len(ev.Problems)+len(ev.Missing) > 0 {
		return fmt.Errorf("%d claim problem(s), %d reference(s) no job reported", len(ev.Problems), len(ev.Missing))
	}
	return nil
}

// summarize renders a run's claims as markdown: verdict counts (with the
// change from base when there is one), what does not check, and which
// claims changed verdict.
func summarize(ev, base *Evidence) string {
	var b strings.Builder
	b.WriteString("### Runtime claims\n\n")
	b.WriteString("Informational: claims never fail a PR. On `main`, problems open the rolling \"claims out of date\" issue.\n\n")
	b.WriteString("| verdict | claims |\n|---|--:|\n")
	for _, v := range []string{"verified", "partial", "failing", "unrun", "gap"} {
		n := ev.Verdicts[v]
		cell := fmt.Sprint(n)
		if base != nil {
			if d := n - base.Verdicts[v]; d != 0 {
				cell += fmt.Sprintf(" (%+d)", d)
			}
		}
		fmt.Fprintf(&b, "| %s | %s |\n", v, cell)
	}
	if len(ev.Problems)+len(ev.Missing) == 0 {
		b.WriteString("\nEvery claim checks and every reference matched a test result.\n")
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n**%s** (%d)\n\n", title, len(items))
		for i, it := range items {
			if i == 30 {
				fmt.Fprintf(&b, "- … and %d more\n", len(items)-30)
				break
			}
			fmt.Fprintf(&b, "- `%s`\n", it)
		}
	}
	list("Claim problems", ev.Problems)
	list("References no job reported (renamed or removed test?)", ev.Missing)
	if base != nil {
		was := map[string]string{}
		for _, c := range base.Claims {
			was[c.ID] = c.Verdict
		}
		var changed []string
		for _, c := range ev.Claims {
			if w, ok := was[c.ID]; ok && w != c.Verdict {
				changed = append(changed, fmt.Sprintf("%s: %s → %s", c.ID, w, c.Verdict))
			}
		}
		list("Verdicts changed since main", changed)
	}
	return b.String()
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
