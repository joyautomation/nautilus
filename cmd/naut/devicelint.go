package main

// naut devices lint <profile>: what writing a profile from a manual gets
// wrong (docs/design/device-library.md §5).

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/virtdev"
)

// runDevicesLint checks one profile: a built-in id, or a path to a
// project's own .yaml. Errors (a profile that does not parse, an address
// two points share, a simulator that does not compile) make it exit 1;
// warnings (a document nothing cites, unverified rows) do not.
func runDevicesLint(args []string, w io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, "usage: naut devices lint <profile>   (a built-in id, or a path ending in .yaml)\n")
		return 2
	}
	ref := args[0]
	wd, _ := os.Getwd()
	p, err := devprofile.ResolveProfile(wd, ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut devices lint:", err)
		return 1
	}
	failed := false
	for _, f := range p.Lint() {
		fmt.Fprintf(w, "%s: %s\n", p.ID, f)
		failed = failed || f.Error
	}
	if err := lintSimulator(p, ref, wd); err != nil {
		fmt.Fprintf(w, "%s: error: simulator: %v\n", p.ID, err)
		failed = true
	}
	if failed {
		return 1
	}
	fmt.Fprintf(w, "%s: ok\n", p.ID)
	return 0
}

// lintSimulator compiles the profile's simulator the way `naut devices up`
// does: one device of the profile, its block wired to the points, the plant
// built into a runtime. A profile with no simulator has nothing to compile.
func lintSimulator(p *devprofile.Profile, ref, wd string) error {
	if p.Simulator == nil {
		return nil
	}
	if strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
		if !filepath.IsAbs(ref) {
			ref = filepath.Join(wd, ref)
		}
	}
	set, err := virtdev.ParseSet([]byte(fmt.Sprintf("devices:\n  - { name: LINT, profile: %q }\n", ref)), wd)
	if err != nil {
		return err
	}
	plant, err := virtdev.NewPlant(set)
	if err != nil {
		return err
	}
	if _, err := runtime.New(plant.Options()); err != nil {
		return fmt.Errorf("the plant does not compile: %v", err)
	}
	return nil
}
