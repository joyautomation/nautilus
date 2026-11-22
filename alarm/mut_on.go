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
//   - carry_unclamped: a carried or adopted time ahead of the clock is
//     kept (a wait in the future pages nobody);
//   - adopt_after_suppress: suppression since the restore does not stop
//     an adoption;
//   - carry_ack_resaved_after_false: a held carry re-saves its ack after
//     a false reading;
//   - carry_wait_reset: a carried unacknowledged occurrence starts its
//     wait afresh, so escalation restarts at level 1.
//
// And of live detection (#1249):
//
//   - chatter_never: no activation ever chatters;
//   - chatter_no_merge: an overlapping window does not continue an
//     episode, so one burst reads as several;
//   - flood_no_hysteresis: a flood ends the moment the count is at the
//     threshold, not a full window later;
//   - grace_counts: activations inside a standalone start's grace count;
//   - restart_counts: a condition that stood through a re-baseline
//     counts when it activates;
//   - first_out_repeats: a flood's first out names one alarm as often as
//     it activated.
var mutOn = os.Getenv("QUORUM_MUT")

func mutAlarm(name string) bool { return name == mutOn }
