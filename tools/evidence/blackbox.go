package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// runBlackbox reports, per claims file, how many claims cite one of the
// independent tester's checks (`go ./tools/blackbox TestBlackbox/<id>`):
// the black-box suite's coverage of what the docs promise, which is the
// coverage a tester that never sees the code can be measured by. Pages
// with no such claim are listed in one line; for the others the claims
// without one are named, since those are what the next brief points at.
func runBlackbox(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("blackbox", flag.ExitOnError)
	root := fs.String("root", ".", "repository root")
	fs.Parse(args)
	files, problems, err := loadClaims(filepath.Join(*root, "docs", "claims"))
	if err != nil {
		return err
	}
	for _, p := range problems {
		fmt.Fprintln(out, "skipped:", p)
	}
	var none []string
	total, covered := 0, 0
	for _, f := range files {
		var missing []string
		n := 0
		for _, c := range f.Claims {
			if citesBlackbox(c) {
				n++
			} else {
				missing = append(missing, c.ID)
			}
		}
		total += len(f.Claims)
		covered += n
		if n == 0 {
			none = append(none, strings.TrimSuffix(f.File, ".yaml"))
			continue
		}
		fmt.Fprintf(out, "%s: %d of %d claims have a black-box check", f.File, n, len(f.Claims))
		if len(missing) > 0 {
			fmt.Fprintf(out, "; without one: %s", strings.Join(missing, " "))
		}
		fmt.Fprintln(out)
	}
	if len(none) > 0 {
		fmt.Fprintf(out, "no black-box check yet: %s\n", strings.Join(none, " "))
	}
	fmt.Fprintf(out, "docs/claims: %d of %d claims have a black-box check\n", covered, total)
	return nil
}

func citesBlackbox(c Claim) bool {
	for _, t := range c.Tests {
		if r, err := parseRef(t); err == nil && r.Kind == "go" && r.Dir == "./tools/blackbox" {
			return true
		}
	}
	return false
}

// blackboxID is the check id a reference names, when it names one of the
// tester's checks (`go ./tools/blackbox TestBlackbox/<id>`).
func blackboxID(r Ref) (string, bool) {
	if r.Kind != "go" || r.Dir != "./tools/blackbox" {
		return "", false
	}
	id, ok := strings.CutPrefix(r.Test, "TestBlackbox/")
	id, _, _ = strings.Cut(id, "/")
	return id, ok && id != ""
}

// loadDispositions reads each check's disposition from
// tools/blackbox/checks.yaml. It decides where a cited check's verdict
// comes from: only `gate` checks run in every CI build, `release` ones run
// in blackbox-release.yml (nightly on main and on each CLI release), and
// `soak`, `manual` and `once` ones nowhere. A missing manifest is an empty
// one, so every black-box reference reads as an unknown check.
func loadDispositions(root string) (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(root, "tools", "blackbox", "checks.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m struct {
		Checks []struct {
			ID          string `yaml:"id"`
			Disposition string `yaml:"disposition"`
		} `yaml:"checks"`
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("tools/blackbox/checks.yaml: %w", err)
	}
	out := map[string]string{}
	for _, c := range m.Checks {
		out[c.ID] = c.Disposition
	}
	return out, nil
}

// resolve looks a reference up in the results of the run that executes
// it. A black-box check outside the gate never runs in this build, so
// reading it here would report "no job reported" on every main run (the
// rolling issue that never closed, #442). A `release` check is answered
// by rel, the last nightly blackbox-release run on main, marked as from
// there; when that run did not report it, it reads release-only. A soak,
// manual or once check runs in no job at all and reads unscheduled. Both
// are informational, not claim problems. A check id the manifest does not
// know (a rename) is looked up here as before, reads missing, and
// checkClaims flags it.
func resolve(ref Ref, res, rel *Results, disp map[string]string) RefResult {
	id, ok := blackboxID(ref)
	d, known := disp[id]
	switch {
	case !ok || !known || d == "gate":
		return res.lookup(ref)
	case d != "release":
		return RefResult{Ref: ref.Raw, Status: Unscheduled, Disposition: d}
	}
	rr := rel.lookup(ref)
	rr.Disposition = d
	if rr.Status == "missing" {
		rr.Status = ReleaseOnly
		return rr
	}
	rr.From = "blackbox-release"
	return rr
}

// The statuses of a black-box reference outside the gate with no verdict:
// a release check no blackbox-release run has reported yet, and a check
// whose disposition (soak, manual, once) runs in no job.
const (
	ReleaseOnly = "release-only"
	Unscheduled = "unscheduled"
)

// ReleaseRun is the blackbox-release run whose results answer the release
// checks, as the workflow records it in its artifact (release-run.json),
// so a verdict carries its age: a nightly GitHub stopped scheduling must
// not read as current.
type ReleaseRun struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Event string `json:"event"`
	Ref   string `json:"ref"`  // the tag, or main@<sha>
	Date  string `json:"date"` // RFC 3339, when the run started
}

const releaseRunFile = "release-run.json"

// readRelease reads a downloaded blackbox-release artifact: the checks'
// go test -json and the run's release-run.json. A run that left an
// artifact but no check results (the runner did not build, the oracles
// did not install) is an error, never a silent fall back to release-only.
func readRelease(dir string) (*Results, *ReleaseRun, error) {
	rel := newResults()
	if _, err := os.Stat(dir); err != nil {
		return rel, nil, nil // no run has left evidence yet
	}
	if err := rel.readDir(dir); err != nil {
		return rel, nil, err
	}
	var run *ReleaseRun
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Name() != releaseRunFile {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		run = &ReleaseRun{}
		return json.Unmarshal(b, run)
	})
	if err != nil {
		return rel, nil, fmt.Errorf("blackbox-release evidence: %w", err)
	}
	for t := range rel.Go["./tools/blackbox"] {
		if strings.HasPrefix(t, "TestBlackbox/") {
			return rel, run, nil
		}
	}
	if run == nil && len(rel.Go)+len(rel.Pkg) == 0 {
		return rel, nil, nil // an empty directory: nothing was downloaded
	}
	which := "the downloaded blackbox-release run"
	if run != nil {
		which = fmt.Sprintf("blackbox-release run %s (%s, %s)", run.URL, run.Ref, run.Date)
	}
	return rel, run, fmt.Errorf("%s reported no check results (did it build?); its release checks read release-only", which)
}
