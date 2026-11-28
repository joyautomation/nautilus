//go:build !oplogtest

package main

import "github.com/joyautomation/nautilus/oplog"

// testLogOptions is the oplogtest build's hook on the pair's and the
// witness's log options (logseam_on.go); in every other build, a release
// included, it does nothing.
func testLogOptions(string, *oplog.Options) {}
