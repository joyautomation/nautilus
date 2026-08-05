package acceptance_test

// The YAML tier, exercised against the same example the Go tests use. If
// these agree with heated_tank_test.go, the format expresses everything
// the Go form does — which is the whole claim.

import (
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

func TestHeatedTankSuite(t *testing.T) {
	fsys := os.DirFS(nogo)
	proj, err := project.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no tests discovered — the example should carry a *_test.yaml")
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
			continue
		}
		t.Logf("ok   %-52s %d scans, %v", r.Name, r.Scans, r.Elapsed)
	}
}

// Discovery, and the promise that a *_test.yaml never reaches a deployed
// controller.
func TestDiscovery(t *testing.T) {
	paths, err := acceptance.DiscoverSuites(os.DirFS(nogo))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], acceptance.SuffixTest) {
		t.Fatalf("discovered %v, want one *_test.yaml", paths)
	}
}
