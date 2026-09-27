package main

import (
	"fmt"
	"os"
)

// `naut prometheus` — the commissioning side of the prometheus driver: import,
// browse, serve, tags (docs/design/it-drivers.md §8). Stub: replaced by the
// prometheus package's own implementation.
func runPrometheus(args []string) int {
	fmt.Fprintf(os.Stderr, "naut prometheus: not implemented yet — see docs/design/it-drivers.md §8\n")
	return 2
}
