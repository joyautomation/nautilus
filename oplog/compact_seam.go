//go:build oplogtest && !js

package oplog

import "time"

// TestCompaction is the oplogtest build's seam for a test that runs the
// production composition (cmd/naut's pair rig: the server's keep decision,
// the runtime, the alarm view, the notes) through committed compactions
// while the shipped build leaves compaction off (#1455 PR 2b-1.9). It sets
// the keep-rules version and shortens the decision's timings. Only in a
// build with the oplogtest tag, never a release: cmd/naut's
// TestTheCompactionSeamIsNotInAReleaseBuild checks the build constraints.
type TestCompaction struct {
	Rules      int           // the keep-rules version (Options.CompactRules)
	Every      time.Duration // between two looks
	Margin     time.Duration // the tail never dropped
	Gap        time.Duration // between two compactions
	MinEntries int           // the part older than the margin compacts from this many entries
}

// Apply sets the version and the timings on o, and carries a compaction
// out at once (no random wait before a copy rewrites its file).
func (c TestCompaction) Apply(o *Options) {
	o.CompactRules = c.Rules
	o.decide = decideLimits{every: c.Every, margin: c.Margin, gap: c.Gap, minEntries: c.MinEntries}
	o.lim.compactDelay = func() time.Duration { return 0 }
	o.lim.compactRetry = 100 * time.Millisecond
}
