// `nautilus test`: acceptance tests for a manifest project, with no Go and
// no toolchain. The suites are `*_test.yaml` beside nautilus.yaml; the
// runtime drives them in virtual time, so a ten-second interlock delay and
// a loop's settling time are asserted exactly, in milliseconds.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

func runTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	run := fs.String("run", "", "only run tests whose name matches this regexp")
	verbose := fs.Bool("v", false, "print virtual elapsed time and scan counts for passing tests")
	asJSON := fs.Bool("json", false, "emit one NDJSON event per test (for editors and CI tooling)")
	list := fs.Bool("list", false, "list the tests (suite, name, line) without running them")
	manifest := fs.String("m", "", manifestFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	fsys := os.DirFS(dir)

	// A compile failure is a suite failure, carrying the diagnostic: there
	// is no point reporting assertions about a program that does not build.
	// Listing skips it — an editor asks what tests exist while the program
	// is still mid-edit, and answering "it doesn't compile" would empty the
	// test explorer on every keystroke.
	proj, err := project.Load(fsys, *manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus test:", err)
		return 1
	}
	if !*list {
		if _, err := runtime.New(proj.Runtime); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus test: compile:", err)
			return 1
		}
	}

	paths, err := acceptance.DiscoverSuites(fsys)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus test:", err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Fprintf(os.Stderr, "nautilus test: no *%s files in %s\n", acceptance.SuffixTest, dir)
		return 1
	}

	var filter *regexp.Regexp
	if *run != "" {
		if filter, err = regexp.Compile(*run); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus test: bad -run pattern:", err)
			return 2
		}
	}

	var results []acceptance.Result
	for _, p := range paths {
		suite, err := acceptance.LoadSuite(fsys, p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus test:", err)
			return 1
		}
		if filter != nil {
			kept := suite.Tests[:0]
			for _, t := range suite.Tests {
				if filter.MatchString(t.Name) {
					kept = append(kept, t)
				}
			}
			if len(kept) == 0 {
				continue
			}
			suite.Tests = kept
		}
		if *list {
			listSuite(suite)
			continue
		}
		// The manifest's own alarms, over each test's own runtime and
		// virtual clock, with an in-memory journal and no notifiers — a
		// test must never write to the site's alarm database.
		rs, err := acceptance.RunSuite(suite, proj.Runtime, acceptance.WithAlarms(proj.AlarmEngine))
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus test:", err)
			return 1
		}
		results = append(results, rs...)
	}

	if *list {
		return 0
	}
	if len(results) == 0 {
		fmt.Fprintf(os.Stderr, "nautilus test: no tests matched %q\n", *run)
		return 1
	}
	var failed int
	if *asJSON {
		failed = acceptance.ReportJSON(os.Stdout, results)
	} else {
		failed = acceptance.Report(os.Stdout, results, *verbose)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// listSuite emits one NDJSON record per test without running anything —
// what an editor's test explorer needs to populate its tree. Same shape as
// a result minus the outcome, so a consumer can key on (suite, name)
// across both.
func listSuite(s *acceptance.Suite) {
	enc := json.NewEncoder(os.Stdout)
	for _, t := range s.Tests {
		_ = enc.Encode(struct {
			Suite string `json:"suite"`
			Name  string `json:"name"`
			Line  int    `json:"line"`
		}{s.Path, t.Name, t.Line})
	}
}
