package main

import (
	"fmt"
	"os"
)

// `naut redfish` — the commissioning side of the redfish driver: import,
// browse, serve, tags (docs/design/it-drivers.md §8). Stub: replaced by the
// redfish package's own implementation.
func runRedfish(args []string) int {
	fmt.Fprintf(os.Stderr, "naut redfish: not implemented yet — see docs/design/it-drivers.md §8\n")
	return 2
}
