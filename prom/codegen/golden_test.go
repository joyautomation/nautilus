package codegen

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/joyautomation/nautilus/hw"
)

func updateFlag() *bool { return flag.Bool("update", false, "rewrite the testdata golden files") }

func goldenCompare(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./prom/codegen -run Golden -update` to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s drifted from the golden:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func hwTypesST() ([]byte, error) { return hw.TypesST("prometheus") }
