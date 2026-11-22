package server

// sessionlog.go carries browser sessions through a pair's operator log
// (#716), so an operator who is signed in stays signed in across a takeover,
// when they most need to acknowledge an alarm.
//
// What travels is the session's record (oplog.SessionEntry): the SHA-256 of
// its random id, who it is, when it ends. The id itself never does, so the
// log is not a way to sign in, and no shared signing key is needed: the new
// leader recognises the cookie by its hash.
//
// Rules, in the order they matter:
//
//   - Signing in never waits for the log. A sign-in is issued at once; if the
//     log cannot commit it (read-only, no majority, an unsynced takeover) the
//     session stays local: valid on this controller only, journalled, and
//     replicated later when the log comes back. An operator locked out in an
//     upset is worse than a session that did not replicate.
//   - Ending is fail-closed. A sign-out, a revoked password, an ended user
//     ends the session here at once and proposes an end entry; the sign-out
//     waits (briefly) for it to commit, so that a takeover right after cannot
//     bring the session back. An end that cannot commit is retried until the
//     session would have expired, and the end always wins over an open of the
//     same session, whatever order they arrive in.
//   - A copy of the log that is not known complete honours no session that
//     came through it (it may lack the end), only local ones.
//   - Elevation is not carried: after a takeover, an elevated action asks for
//     the password again.

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/oplog"
)

// sessionBus is the operator log as the session log uses it. *oplog.Node is
// one.
type sessionBus interface {
	Propose(ctx context.Context, e oplog.Entry) (oplog.Entry, error)
	Synced() bool
}

// Timings of the session log's proposals, variables so a test shortens them.
var (
	sessionProposeWait = 3 * time.Second         // one proposal's wait
	sessionEndWait     = 1500 * time.Millisecond // how long a sign-out waits for its end to commit
	sessionRetryEvery  = 10 * time.Second        // between a refused proposal and the next
)

// SessionLog replicates the sessions of one controller through the pair's
// operator log and applies the log's session entries to its store.
type SessionLog struct {
	bus atomic.Pointer[sessionBus]
	ss  atomic.Pointer[sessions]

	// local is told once when a session had to stay local (the log did not
	// commit its open on the first try).
	local atomic.Pointer[func(session)]
	wg    sync.WaitGroup
}

// NewSessionLog is a pair's session log; Attach gives it the log node.
func NewSessionLog() *SessionLog { return &SessionLog{} }

// Attach sets the log that proposals go to and whose sync state gates
// replicated sessions.
func (l *SessionLog) Attach(b sessionBus) {
	l.bus.Store(&b)
	if ss := l.ss.Load(); ss != nil {
		l.hookStore(ss)
	}
}

// OnLocal sets what is told when a session stays local.
func (l *SessionLog) OnLocal(f func(session)) { l.local.Store(&f) }

// bind is the server's session store taking this log.
func (l *SessionLog) bind(ss *sessions) {
	l.ss.Store(ss)
	ss.mu.Lock()
	ss.rep = l
	ss.mu.Unlock()
	l.hookStore(ss)
}

func (l *SessionLog) hookStore(ss *sessions) {
	ss.mu.Lock()
	ss.synced = func() bool {
		b := l.bus.Load()
		return b != nil && (*b).Synced()
	}
	ss.mu.Unlock()
}

// Apply takes a committed entry if it is a session change, and reports
// whether it was (an Applier composed of several hands the others on). A
// build from before sessions keeps the entry whole and ignores its kind.
func (l *SessionLog) Apply(e oplog.Entry) (bool, error) {
	if e.Kind != oplog.KindSession {
		return false, nil
	}
	ss := l.ss.Load()
	body, ok := e.SessionBody()
	if !ok || ss == nil {
		return true, nil // unreadable or no store yet: nothing to carry out
	}
	switch body.Op {
	case oplog.SessionOpen:
		ss.adopt(body)
	case oplog.SessionEnd:
		ss.endByLog(body)
	}
	return true, nil
}

func entryOf(se session, op string) oplog.Entry {
	return oplog.SessionEntryOf("system", oplog.SessionEntry{
		Op: op, Hash: se.hash, User: se.user, OIDC: se.oidc, Issuer: se.issuer, Sub: se.sub, Groups: se.groups,
		Created: se.created.UnixMilli(), Expires: se.expires.UnixMilli(), Cred: se.cred,
	})
}

// propose sends e until it commits, the session ends or expires (stop
// says so), or the log is gone. It reports whether it committed on the first
// try's wait; the retries go on in the background.
func (l *SessionLog) propose(e oplog.Entry, expires time.Time, stop func() bool, firstDone chan<- bool) {
	defer l.wg.Done()
	first := true
	for {
		b := l.bus.Load()
		if b != nil {
			ctx, cancel := context.WithTimeout(context.Background(), sessionProposeWait)
			_, err := (*b).Propose(ctx, e)
			cancel()
			if err == nil {
				if first {
					firstDone <- true
				}
				return
			}
		}
		if first {
			first = false
			firstDone <- false
		}
		if time.Now().After(expires) || stop() {
			return
		}
		time.Sleep(sessionRetryEvery)
	}
}

// opened proposes a new session's open entry in the background: the
// sign-in never waits for it. If the first try fails the session is local,
// and the hook says so.
func (l *SessionLog) opened(se session) {
	ss := l.ss.Load()
	done := make(chan bool, 1)
	l.wg.Add(1)
	go l.propose(entryOf(se, oplog.SessionOpen), se.expires, func() bool { return ss == nil || !ss.has(se.hash) }, done)
	go func() {
		if ok := <-done; !ok {
			if f := l.local.Load(); f != nil && ss != nil && ss.isLocal(se.hash) {
				(*f)(se)
			}
		}
	}()
}

// endedSessions proposes an end entry for each session and waits up to
// sessionEndWait for them to commit; what has not by then goes on in the
// background.
func (l *SessionLog) endedSessions(ended []session) {
	if len(ended) == 0 {
		return
	}
	dones := make([]chan bool, len(ended))
	for i, se := range ended {
		dones[i] = make(chan bool, 1)
		l.wg.Add(1)
		go l.propose(entryOf(se, oplog.SessionEnd), se.expires, func() bool { return false }, dones[i])
	}
	deadline := time.After(sessionEndWait)
	for _, d := range dones {
		select {
		case <-d:
		case <-deadline:
			return
		}
	}
}

// Wait blocks until every proposal goroutine has finished (a test's).
func (l *SessionLog) Wait() { l.wg.Wait() }
