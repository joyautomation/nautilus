//go:build quorum_mut

package escalate

import "os"

// Mutations of chatter and flood paging (#1249 slice 3), switched on like
// the server's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - chatter_pages_per_bounce: a chattering alarm's bounce clocks are
//     not held, so each bounce pages as before;
//   - chatter_pin_latched: a latched alarm whose wait began with the
//     episode is pinned too (and loses its own clock);
//   - chatter_clock_per_wait: the pinned clock ends with the wait, so a
//     bounce through normal sends the notice again;
//   - regroup_unbounded: a policy step's batches are not spaced, so each
//     alarm that falls due gets its own grouped page;
//   - group_covers_on_failure: a grouped send that failed covers its
//     alarms, so nobody is paged about them;
//   - group_individual_ignored: critical and safety alarms are grouped
//     like the rest;
//   - notice_carries_ack: a chatter notice carries the ack link and
//     code (a grouped page has no occurrence to bind either way);
//   - takeover_resend_individual: a new leadership's first look under a
//     flood resends each covered alarm on its own;
//   - chatter_policy_at_start: the pinned clock takes its policy at the
//     episode's start and keeps it (Fable review: a when: window missed);
//   - chatter_joins_late: the pinned clock skips to the latest due level,
//     so a level 1 no bounce reached is never sent;
//   - chatter_end_burst: back on its own clock after a chatter, an alarm
//     sends again every level its chatter notices sent;
//   - chatter_end_skips_new: an activation after the chatter ended skips
//     its due levels as if the notices had covered it;
//   - group_drop_lost: a grouped send the queue could not take is lost
//     once another of its batch is delivered;
//   - released_batch_floors: a grouped page released before it left
//     still holds its policy step's next one back 5 minutes;
//   - safety_case_exact: only the class spelled "safety" pages on its own;
//   - chatter_repeats_burst: a pinned clock that starts or moves late
//     sends every due repeat of a level at once.
var mutOn = os.Getenv("QUORUM_MUT")

func mutEscalate(name string) bool { return name == mutOn }
