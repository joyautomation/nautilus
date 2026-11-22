//go:build js

package project

import "io/fs"

// The I/O checkout (#719) is controller-only: the browser build reads no
// sheet, and a checkout: section there is ignored.
func loadCheckoutSheet(fs.FS, string) (any, error) { return nil, nil }
