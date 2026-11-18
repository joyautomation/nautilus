package testmqtt

import (
	"os/exec"
	"strings"
	"testing"
)

// testmqtt is for tests: nothing shipped may import it. go list's .Imports
// leaves out _test.go files, so any package listing it here imports it from
// shipped code.
func TestOnlyTestsImportTestmqtt(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	cmd := exec.Command("go", "list", "-e", "-f", `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`, "./...")
	cmd.Dir = "../.." // the module root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const self = "github.com/joyautomation/nautilus/internal/testmqtt"
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		n++
		for _, imp := range fields[1:] {
			if imp == self {
				t.Errorf("%s imports %s outside its tests: it is test-only", fields[0], self)
			}
		}
	}
	if n < 50 {
		t.Fatalf("go list found only %d packages: the walk lost the module", n)
	}
}
