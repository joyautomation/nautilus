package project

import (
	"os"
	"testing"

	"github.com/joyautomation/nautilus/lang/lint"
)

// The structure rules run on every keystroke in the editor: one parse of
// the file, no compile. This keeps an eye on the cost for the largest
// example diagrams.
func BenchmarkStructure(b *testing.B) {
	files := []string{
		"../../examples/lift-station/permissives.ld",
		"../../examples/batch-skid/dosing.fbd",
		"../../examples/batch-skid/phases.sfc",
	}
	srcs := make([]string, len(files))
	for i, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			b.Fatal(err)
		}
		srcs[i] = string(raw)
	}
	cfg := lint.Defaults()
	for _, i := range []int{0, 1, 2} {
		b.Run(files[i][len("../../examples/"):], func(b *testing.B) {
			for range b.N {
				Structure(files[i], srcs[i], cfg)
			}
		})
	}
}
