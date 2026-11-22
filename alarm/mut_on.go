//go:build quorum_mut

package alarm

import "os"

// Mutations of the occurrence rules (#715 PR 1), switched on like the
// server's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - occurrence_per_activation: a flap from unack-rtn back to active
//     draws a new occurrence, so a link sent for the wait goes dead;
//   - ack_any_occurrence: an ack naming another occurrence is applied;
//   - carry_on_false: a carried occurrence continues although the
//     condition read false after the grace;
//   - carry_ack_after_false: a carried ack rides a false reading inside
//     the grace;
//   - carry_unbounded: an unreadable condition holds a carried
//     occurrence forever;
//   - carry_through_enable: enable false does not end a carried
//     occurrence;
//   - carry_end_silent: an acknowledged carried occurrence that ends
//     leaves no journal note;
//   - adopt_off: a committed ack never adopts the occurrence that stood
//     through an unsynced takeover;
//   - carry_wait_reset: a carried unacknowledged occurrence starts its
//     wait afresh, so escalation restarts at level 1.
var mutOn = os.Getenv("QUORUM_MUT")

func mutAlarm(name string) bool { return name == mutOn }
