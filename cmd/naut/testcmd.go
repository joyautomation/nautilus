// `naut test`: acceptance tests for a manifest project, with no Go and
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

	"context"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/logix/facade"
	"github.com/joyautomation/nautilus/runtime"
)

// liveTarget is the facade a `--target logix` run reads and writes
// through; opened once per invocation, on the first suite.
var liveTarget *acceptance.Live

// liveLogix browses the project's Logix controller and returns the live
// resource the scenarios run against. The program's tags are served as
// <Program>_<Tag>, which Resolve maps scenario names onto.
func liveLogix(dir string, proj *project.Project) (*acceptance.Live, error) {
	if liveTarget != nil {
		return liveTarget, nil
	}
	p, err := loadLogixProject(dir)
	if err != nil {
		return nil, err
	}
	if p.host == "" {
		return nil, fmt.Errorf("target.logix.host is not set: the controller's EtherNet/IP address")
	}
	poll := 100 * time.Millisecond
	fmt.Fprintf(os.Stderr, "browsing %s (slot %d)...\n", p.host, p.slot)
	bctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	f, err := facade.New(bctx, facade.Options{Host: p.host, Slot: p.slot, Port: p.port, Poll: poll})
	if err != nil {
		return nil, err
	}
	go f.Run(context.Background())
	rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rcancel()
	if err := f.Ready(rctx); err != nil {
		return nil, fmt.Errorf("the controller at %s answered no poll in 30 s", p.host)
	}
	program := p.target.Program
	if program == "" {
		program = runtime.POUOf(p.source)
	}
	fmt.Fprintf(os.Stderr, "live: %d tags, program %s, poll %s\n", f.Tags(), program, poll)
	scan := time.Duration(p.target.PeriodMs) * time.Millisecond
	// Every test starts with the inputs at their manifest seeds, as the
	// virtual runtime starts each test — the controller would otherwise
	// hand the next test whatever the last one left.
	seeds := map[string]any{}
	if m, err := project.ReadManifest(os.DirFS(dir), ""); err == nil {
		for _, tg := range m.Tags {
			if tg.Role == "input" && tg.Init != nil {
				seeds[tg.Name] = tg.Init
			}
		}
	}
	liveTarget = &acceptance.Live{
		Runtime: f.Runtime(), Write: f.Write, Poll: poll, Scan: scan,
		Resolve: acceptance.ResolveLogix(f.Runtime(), program), Libraries: proj.Runtime.Libraries,
		Heartbeat: p.target.Side.Heartbeat, Seeds: seeds,
	}
	if liveTarget.Heartbeat != "" {
		if _, err := f.Runtime().Tags().ReadGlobal(liveTarget.Heartbeat); err != nil {
			fmt.Fprintf(os.Stderr, "live: the controller has no heartbeat tag %s (deploy the side code first); scans: will be time-based\n", liveTarget.Heartbeat)
			liveTarget.Heartbeat = ""
		}
	}
	return liveTarget, nil
}

func runTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	run := fs.String("run", "", "only run tests whose name matches this regexp")
	verbose := fs.Bool("v", false, "print virtual elapsed time and scan counts for passing tests")
	asJSON := fs.Bool("json", false, "emit one NDJSON event per test (for editors and CI tooling)")
	list := fs.Bool("list", false, "list the tests (suite, name, line) without running them")
	manifest := fs.String("m", "", manifestFlagUsage)
	target := fs.String("target", "", "run the scenarios against the deployed controller instead of the nautilus runtime (experimental): \"logix\" drives the project's target: logix controller over EtherNet/IP, in real time")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target != "" && *target != "logix" {
		fmt.Fprintf(os.Stderr, "naut test: unknown target %q (the targets are: logix)\n", *target)
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
		fmt.Fprintln(os.Stderr, "naut test:", err)
		return 1
	}
	if !*list {
		if _, err := runtime.New(proj.Runtime); err != nil {
			fmt.Fprintln(os.Stderr, "naut test: compile:", err)
			return 1
		}
	}

	paths, err := acceptance.DiscoverSuites(fsys)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut test:", err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Fprintf(os.Stderr, "naut test: no *%s files in %s\n", acceptance.SuffixTest, dir)
		return 1
	}

	var filter *regexp.Regexp
	if *run != "" {
		if filter, err = regexp.Compile(*run); err != nil {
			fmt.Fprintln(os.Stderr, "naut test: bad -run pattern:", err)
			return 2
		}
	}

	var results []acceptance.Result
	for _, p := range paths {
		suite, err := acceptance.LoadSuite(fsys, p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut test:", err)
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
		var rs []acceptance.Result
		if *target == "logix" {
			live, lerr := liveLogix(dir, proj)
			if lerr != nil {
				fmt.Fprintln(os.Stderr, "naut test:", lerr)
				return 1
			}
			rs, err = acceptance.RunSuiteLive(suite, *live)
		} else {
			// The manifest's own alarms, over each test's own runtime and
			// virtual clock, with an in-memory journal and no notifiers — a
			// test must never write to the site's alarm database.
			rs, err = acceptance.RunSuite(suite, proj.Runtime, acceptance.WithAlarms(proj.AlarmEngine))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut test:", err)
			return 1
		}
		results = append(results, rs...)
	}

	if *list {
		return 0
	}
	if len(results) == 0 {
		fmt.Fprintf(os.Stderr, "naut test: no tests matched %q\n", *run)
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
