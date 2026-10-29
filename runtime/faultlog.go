package runtime

import (
	"log/slog"
	"time"
)

// Fault transitions in the journal.
//
// A program that faults every scan used to be silent: the scan bumped
// LogicErrors and overwrote LastError, and the only way to learn that the
// main task had been failing since 03:12 was to open the diagnostics page
// and happen to look. A PLC does better — Logix puts a major/minor fault
// in the controller log with a timestamp — and so must this. But logging
// the fault on every scan is the opposite failure: a 10 ms task faulting
// for an hour is 360,000 identical lines, which buries the one that
// mattered and costs the scan a formatted log record each time.
//
// So the journal hears about TRANSITIONS, per task and per kind (logic,
// I/O):
//
//   - one WARN when a task goes from healthy to faulting, carrying the
//     error;
//   - one INFO when it recovers, carrying how many scans faulted and for
//     how long;
//   - while it stays faulting, a WARN when the error MESSAGE changes (a
//     different tag went undefined, a different device stopped answering)
//     — but at most once per faultChangeEvery per task and kind, so a
//     message that flaps between two values every scan cannot flood. A
//     change suppressed by that limit is not lost: the next scan after the
//     window that still differs from the last logged message logs it,
//     with a count of the changes skipped in between.
//
// The decision is made under the lock the stats already take (it is a few
// comparisons on fields that live beside the counters), and the slog call
// happens after that lock is released, so a slow log sink never extends a
// lock the HMI's Stats() contends on. A healthy scan pays one boolean test
// and allocates nothing; a faulting scan's error string was already being
// built for LastError.

// faultChangeEvery bounds how often a task's still-faulting error may be
// re-logged because its message changed.
const faultChangeEvery = time.Minute

// faultTrack is one task's fault state for one kind of fault (logic, I/O).
// It lives beside the stats it shadows and is guarded by the same mutex.
type faultTrack struct {
	faulting   bool
	since      time.Time // first faulting scan of this episode
	scans      uint64    // faulting scans in this episode
	last       string    // this episode's most recent message
	logged     string    // the last message the journal was given
	loggedAt   time.Time // when it was given
	suppressed uint64    // message changes not logged since loggedAt
}

// faultEvent is what one scan's fault state asks the journal to say, decided
// under the stats lock and emitted after it. The zero value says nothing.
type faultEvent struct {
	kind       faultKind
	msg        string
	scans      uint64
	dur        time.Duration
	suppressed uint64
}

type faultKind uint8

const (
	faultNone faultKind = iota
	faultStart
	faultChanged
	faultRecovered
)

// observe folds one scan's outcome into the track and returns what, if
// anything, the journal should hear. msg is the scan's error text ("" for
// a healthy scan); now is the wall time of the scan.
func (f *faultTrack) observe(msg string, now time.Time) faultEvent {
	if msg == "" {
		if !f.faulting {
			return faultEvent{} // the steady state: one test, no work
		}
		ev := faultEvent{kind: faultRecovered, msg: f.last, scans: f.scans, dur: now.Sub(f.since)}
		*f = faultTrack{}
		return ev
	}
	if !f.faulting {
		*f = faultTrack{faulting: true, since: now, scans: 1, last: msg, logged: msg, loggedAt: now}
		return faultEvent{kind: faultStart, msg: msg}
	}
	f.scans++
	f.last = msg
	if msg == f.logged {
		return faultEvent{}
	}
	if now.Sub(f.loggedAt) < faultChangeEvery {
		f.suppressed++
		return faultEvent{}
	}
	ev := faultEvent{kind: faultChanged, msg: msg, scans: f.scans, dur: now.Sub(f.since), suppressed: f.suppressed}
	f.logged, f.loggedAt, f.suppressed = msg, now, 0
	return ev
}

// logFault writes one fault transition to the journal. what names the kind
// of fault ("logic" — the program's scan failed; "I/O" — the driver read or
// write failed and the scan ran on last-known values).
func (r *Runtime) logFault(task, what string, ev faultEvent) {
	if ev.kind == faultNone {
		return
	}
	log := r.log
	if log == nil {
		log = slog.Default()
	}
	switch ev.kind {
	case faultStart:
		log.Warn("runtime: "+what+" fault", "task", task, "error", ev.msg)
	case faultChanged:
		args := []any{"task", task, "error", ev.msg, "scans", ev.scans, "for", ev.dur.Round(time.Millisecond)}
		if ev.suppressed > 0 {
			// Changes inside the rate-limit window: counted, not lost.
			args = append(args, "unlogged_changes", ev.suppressed)
		}
		log.Warn("runtime: "+what+" fault changed", args...)
	case faultRecovered:
		log.Info("runtime: "+what+" fault cleared", "task", task, "scans", ev.scans,
			"for", ev.dur.Round(time.Millisecond), "last_error", ev.msg)
	}
}

// errText is an error's message, "" for nil — the form faultTrack takes. An
// error whose message is empty still counts as a fault.
func errText(err error) string {
	if err == nil {
		return ""
	}
	if s := err.Error(); s != "" {
		return s
	}
	return "(error with no message)"
}
