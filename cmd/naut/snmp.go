package main

import (
	"fmt"
	"os"
)

// `naut snmp` — the commissioning side of the snmp driver: import,
// browse, serve, tags (docs/design/it-drivers.md §8). Stub: replaced by the
// snmp package's own implementation.
func runSnmp(args []string) int {
	fmt.Fprintf(os.Stderr, "naut snmp: not implemented yet — see docs/design/it-drivers.md §8\n")
	return 2
}
