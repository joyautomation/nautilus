// Package alarm turns BOOL tags into ISA-18.2 alarm state: an active list,
// acknowledge, shelve, and an append-only journal.
//
// It is a peripheral subsystem in the sense hist/, retain/ and leader/ are:
// the runtime is the scan loop and the tag store, and should not grow an
// opinion about alarm philosophy. Nothing here imports runtime or server —
// the engine reads the tag store through one injected function and is driven
// by explicit Evaluate calls, so internal/project can wire it in a dozen
// lines and an acceptance test can drive it against a stopped virtual clock.
//
// The three pieces, in dependency order:
//
//   - Def is one alarm definition: an id, a BOOL condition path ("Tag" —
//     either a flat tag or "tag.member"), a priority and the operator
//     policy (ack-required, auto-clear, shelvable, on/off delay). Rule
//     generates Defs in bulk by matching a struct TYPE and member, which is
//     what makes 14 rules cover ~1 850 fleet alarms; Expand materializes
//     rules against a tag list once, at compose time, never per scan.
//
//   - Engine holds one instance of state per Def and runs the ISA-18.2
//     transition table on each Evaluate. It reads through Options.Read and
//     timestamps through Options.Now, so both wall-clock and virtual time
//     work unchanged. A definition whose tag is missing — a site that has
//     never birthed — is Suppressed with a reason, never an error: one dark
//     site must not fault the engine.
//
//   - Journal records what happened (RingJournal in memory, FileJournal as
//     rotated JSONL, PGJournal in Postgres, MultiJournal fanning out) and
//     Notifier ships it somewhere (LogNotifier, WebhookNotifier). Both run
//     off the scan goroutine; a slow endpoint drops events with a counter
//     rather than stalling the scan.
//
// Ack and shelve are operator state and are never written back to the edge —
// an ack is meaningless to a site that is offline, which is exactly when
// operators ack most. They persist through retain instead: RetainedAlarms
// and RestoreAlarms move a map[string]retain.AlarmRetain in and out, so a
// restart or a standby takeover cannot resurrect acked alarms as unacked.
// Active and RTN are not retained; they re-derive from the field.
package alarm
