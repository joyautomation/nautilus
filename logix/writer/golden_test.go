package writer

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
)

var update = flag.Bool("update", false, "rewrite the golden L5X snapshots")

// Golden snapshots: the emitted L5X, normalized with lang/l5x.Normalize,
// for each fixture. Any change to what the writer emits — a mnemonic, a
// tag attribute, the envelope — is a diff in review.
//
//	go test ./logix/writer/ -update
func TestGolden(t *testing.T) {
	cases := map[string]Options{
		"demoline": demoOpts(),
		"subset":   {},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			doc, diags, err := Write(fixture(t, name+".ld"), opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(diags) > 0 {
				t.Fatal(joinDiags(diags))
			}
			got := l5x.Normalize(doc, l5x.NormalizeOptions{})
			path := filepath.Join("testdata", name+".golden.L5X")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if string(got) != string(want) {
				t.Errorf("emitted L5X differs from %s (run with -update if the change is intended)\n%s", path, firstDiff(string(want), string(got)))
			}
		})
	}
}

func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range wl {
		if i >= len(gl) {
			return "got ends at line " + itoa(i)
		}
		if wl[i] != gl[i] {
			return "line " + itoa(i+1) + "\n want: " + wl[i] + "\n  got: " + gl[i]
		}
	}
	if len(gl) > len(wl) {
		return "got has " + itoa(len(gl)-len(wl)) + " extra line(s)"
	}
	return ""
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n%10))) }
