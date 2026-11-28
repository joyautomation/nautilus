//go:build oplogtest

package main

import "github.com/joyautomation/nautilus/oplog"

// testLogTweak, set by a test in an oplogtest build, changes the options of
// every operator-intent log this process opens: which is "pair" (a
// controller's) or "witness". The compaction rig sets the keep-rules
// version and short timings through it (oplog.TestCompaction, #1455 PR
// 2b-1.9). Never in a release: logseam_off.go is.
var testLogTweak func(which string, o *oplog.Options)

func testLogOptions(which string, o *oplog.Options) {
	if testLogTweak != nil {
		testLogTweak(which, o)
	}
}
