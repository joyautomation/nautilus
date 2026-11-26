package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/joyautomation/nautilus/logix/logixd"
)

// The point of no return (#1404). Everything a deploy does before its
// first write to the controller (the upload, the mode read, the import
// and build on the agent's files) can be stopped by its caller, and a
// stopped deploy changed nothing. The write itself (a download, an online
// import with FinalizeEdits) and everything after it (the mode read, going
// offline, the verify) run on committed(ctx): the caller's cancel, a
// browser that closed its tab, no longer stops them, and only the deadline
// does. Whether the controller-side work started decides whether it
// finishes, not whether a client stayed connected.
//
// Past that point a deploy can no longer fail cleanly: a failure there
// (the deadline, an agent that stopped answering, a verify that could not
// read the controller or found it different) leaves the controller holding
// the new program, the old one, or neither. That is OutcomeUnknownError,
// never a plain error that reads as "nothing changed".

// committed is ctx without its cancel but with its deadline: the context
// for a write to the controller and everything after it.
func committed(ctx context.Context) (context.Context, context.CancelFunc) {
	c := context.WithoutCancel(ctx)
	if d, ok := ctx.Deadline(); ok {
		return context.WithDeadline(c, d)
	}
	return context.WithCancel(c)
}

// OutcomeUnknownError is a deploy that failed after its write to the
// controller began: the controller may hold the new program. Check what
// it runs before anything else.
type OutcomeUnknownError struct {
	// Step is what failed: "download", "online rung import", "verify", …
	Step string
	Err  error
}

func (e *OutcomeUnknownError) Error() string {
	may := "the controller may hold the new program"
	if e.Step == "download" {
		may += ", and be stopped in Program mode"
	}
	return fmt.Sprintf("%s: OUTCOME UNKNOWN, %s: %v. "+
		"Check what it runs before anything else: `naut logix deploy` without --online or --download "+
		"compares the controller with the project and changes nothing", e.Step, may, e.Err)
}

func (e *OutcomeUnknownError) Unwrap() error { return e.Err }

// unsettled says a call to the agent did not settle what it did. Either
// it got no answer (it never arrived, the reply was cut off or
// unreadable, the context ended the wait), or the answer was that the SDK
// or its channel broke (fatal: operation_not_performed, sdk, internal,
// which covers an SDK timeout). Either way the agent may have done the
// call, or be doing it still. A non-fatal answer (operation_failed,
// conflict, not_found, bad_request) and a refused token are the SDK's or
// the agent's word that it did not (tools/logixd/Program.cs, Fail).
func unsettled(err error) bool {
	if err == nil {
		return false
	}
	var e *logixd.Error
	if !errors.As(err, &e) {
		return true // a context error, or anything else that is not the agent's
	}
	if e.Kind == "unreachable" || e.Kind == "bad_reply" {
		return true
	}
	return e.Fatal && e.Kind != "unauthorized"
}

// modeUnread sets the report's ModeWarning when the mode read after the
// write failed: the deploy still succeeds on its verify, which proves the
// program but not the mode. Louder after a download that switched the
// controller to Program mode, which leaves it stopped.
func modeUnread(rep *Report, err error, programMode bool, logf func(string, ...any)) {
	if err == nil {
		return
	}
	rep.ModeWarning = "mode unknown: check whether the controller is in Run"
	if programMode {
		rep.ModeWarning = "MODE UNKNOWN after a download that switched the controller to Program mode: " +
			"it is probably STOPPED; check it and put it back in Run"
	}
	logf("warning: %s", rep.ModeWarning)
}
