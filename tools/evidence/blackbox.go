package main

import (
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
// rolling issue that never closed, #442): it is answered by rel, the last
// blackbox-release run's results, marked as from there, and when that run
// did not report it, it reads release-only, which is informational and not
// a claim problem. A check id the manifest does not know (a rename) is
// looked up here as before, reads missing, and checkClaims flags it.
func resolve(ref Ref, res, rel *Results, disp map[string]string) RefResult {
	id, ok := blackboxID(ref)
	if d, known := disp[id]; !ok || !known || d == "gate" {
		return res.lookup(ref)
	}
	rr := rel.lookup(ref)
	if rr.Status == "missing" {
		rr.Status = ReleaseOnly
		return rr
	}
	rr.From = "blackbox-release"
	return rr
}

// ReleaseOnly is the status of a reference to a black-box check outside
// the gate that no blackbox-release run has reported yet.
const ReleaseOnly = "release-only"
