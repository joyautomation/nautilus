package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
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
