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
//     bring the session back. An end that cannot commit is kept in a file and
//     retried, by one worker, until it commits or the session would have
//     expired (a restart picks it up again). The end always wins over an open
//     of the same session, whatever order they arrive in: every local end
//     leaves a tombstone until the session's expiry.
//   - A copy of the log that is not known complete honours no session that
//     came through it (it may lack the end), only local ones.
//   - Elevation is not carried: after a takeover, an elevated action asks for
//     the password again.

import (
	"context"
	"encoding/json"
	"os"
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
	sessionRetryEvery  = 10 * time.Second        // between sweeps over what has not committed
)

// SessionLog replicates the sessions of one controller through the pair's
// operator log and applies the log's session entries to its store.
type SessionLog struct {
	bus atomic.Pointer[sessionBus]
	ss  atomic.Pointer[sessions]

	// local is told once when a session had to stay local (the log did not
	// commit its open on the first try).
	local atomic.Pointer[func(session)]

	mu      sync.Mutex
	replay  []oplog.SessionEntry          // entries applied before the store was bound
	ends    map[string]oplog.SessionEntry // ends not yet committed, by hash (kept in file)
	opens   map[string]session            // opens not yet committed
	tried   map[string]bool               // opens whose first attempt failed
	file    string
	running bool
	poke    chan struct{} // wakes the worker: something new is owed
	wake    chan struct{} // closed and replaced whenever something commits
	done    chan struct{}
}

// NewSessionLog is a pair's session log; Attach gives it the log node.
func NewSessionLog() *SessionLog {
	return &SessionLog{ends: map[string]oplog.SessionEntry{}, opens: map[string]session{}, tried: map[string]bool{},
		poke: make(chan struct{}, 1), wake: make(chan struct{}), done: make(chan struct{})}
}

// Attach sets the log that proposals go to and whose sync state gates
// replicated sessions.
func (l *SessionLog) Attach(b sessionBus) {
	l.bus.Store(&b)
	if ss := l.ss.Load(); ss != nil {
		l.hookStore(ss)
	}
	l.kick()
}

// OnLocal sets what is told when a session stays local.
func (l *SessionLog) OnLocal(f func(session)) { l.local.Store(&f) }

// SetFile keeps the ends that have not committed in path, and takes up those
// a previous run left: a controller that restarts before an end committed
// still ends that session on the other one.
func (l *SessionLog) SetFile(path string) {
	l.mu.Lock()
	l.file = path
	if b, err := os.ReadFile(path); err == nil {
		var list []oplog.SessionEntry
		if json.Unmarshal(b, &list) == nil {
			for _, e := range list {
				if time.Now().Before(time.UnixMilli(e.Expires)) {
					l.ends[e.Hash] = e
				}
			}
		}
	}
	ss := l.ss.Load()
	owed := make([]oplog.SessionEntry, 0, len(l.ends))
	for _, e := range l.ends {
		owed = append(owed, e)
	}
	l.mu.Unlock()
	if ss != nil {
		for _, e := range owed {
			ss.tomb(e.Hash, time.UnixMilli(e.Expires))
		}
	}
	l.kick()
}

// Close stops the retry worker.
func (l *SessionLog) Close() {
	l.mu.Lock()
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	l.mu.Unlock()
}

// bind is the server's session store taking this log; entries the log
// applied before the store existed (its replay at start) are applied now.
func (l *SessionLog) bind(ss *sessions) {
	ss.mu.Lock()
	ss.rep = l
	ss.mu.Unlock()
	l.hookStore(ss)
	l.mu.Lock()
	replay := l.replay
	l.replay = nil
	l.ss.Store(ss)
	owed := make([]oplog.SessionEntry, 0, len(l.ends))
	for _, e := range l.ends {
		owed = append(owed, e)
	}
	l.mu.Unlock()
	// An end this controller owes the log (it was signed out before a
	// restart, and the end never committed) is a tombstone first: the log's
	// own copy of the open must not bring the session back meanwhile.
	for _, e := range owed {
		ss.tomb(e.Hash, time.UnixMilli(e.Expires))
	}
	for _, e := range replay {
		apply(ss, e)
	}
}

func (l *SessionLog) hookStore(ss *sessions) {
	ss.mu.Lock()
	ss.synced = func() bool {
		b := l.bus.Load()
		return b != nil && (*b).Synced()
	}
	ss.mu.Unlock()
}

func apply(ss *sessions, e oplog.SessionEntry) {
	switch e.Op {
	case oplog.SessionOpen:
		ss.adopt(e)
	case oplog.SessionEnd:
		ss.endByLog(e)
	}
}

// Apply takes a committed entry if it is a session change, and reports
// whether it was (an Applier composed of several hands the others on). A
// build from before sessions keeps the entry whole and ignores its kind.
func (l *SessionLog) Apply(e oplog.Entry) (bool, error) {
	if e.Kind != oplog.KindSession {
		return false, nil
	}
	body, ok := e.SessionBody()
	if !ok {
		return true, nil
	}
	l.mu.Lock()
	ss := l.ss.Load()
	if ss == nil {
		// the log replays at start, before the store exists: keep the
		// entries, in order, for bind
		l.replay = append(l.replay, body)
		l.mu.Unlock()
		return true, nil
	}
	l.mu.Unlock()
	apply(ss, body)
	if body.Op == oplog.SessionOpen { // committed: no longer owed
		l.committed(func() { delete(l.opens, body.Hash); delete(l.tried, body.Hash) })
	}
	return true, nil
}

// committed runs f under the lock and wakes whoever waits for a commit.
func (l *SessionLog) committed(f func()) {
	l.mu.Lock()
	f()
	close(l.wake)
	l.wake = make(chan struct{})
	l.mu.Unlock()
}

func entryOf(se session, op string) oplog.SessionEntry {
	return oplog.SessionEntry{
		Op: op, Hash: se.hash, User: se.user, OIDC: se.oidc, Issuer: se.issuer, Sub: se.sub, Groups: se.groups,
		Created: se.created.UnixMilli(), Expires: se.expires.UnixMilli(), Cred: se.cred,
	}
}

// persistLocked writes the ends owed. l.mu held.
func (l *SessionLog) persistLocked() {
	if l.file == "" {
		return
	}
	list := make([]oplog.SessionEntry, 0, len(l.ends))
	for _, e := range l.ends {
		list = append(list, e)
	}
	b, _ := json.Marshal(list)
	tmp := l.file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	_, werr := f.Write(b)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		_ = os.Rename(tmp, l.file)
	}
}

// kick starts the worker if there is something owed and none is running.
func (l *SessionLog) kick() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running {
		select { // the worker is between sweeps or in one: have it look again at once
		case l.poke <- struct{}{}:
		default:
		}
		return
	}
	if len(l.ends) == 0 && len(l.opens) == 0 {
		return
	}
	select {
	case <-l.done:
		return
	default:
	}
	l.running = true
	go l.work()
}

// work sends what is owed, again and again, until nothing is: one worker for
// every open and end, however many sign-ins there are during an outage.
func (l *SessionLog) work() {
	for {
		l.sweep()
		l.mu.Lock()
		if len(l.ends) == 0 && len(l.opens) == 0 {
			l.running = false
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
		select {
		case <-time.After(sessionRetryEvery):
		case <-l.poke:
		case <-l.done:
			l.mu.Lock()
			l.running = false
			l.mu.Unlock()
			return
		}
	}
}

func (l *SessionLog) propose(e oplog.SessionEntry) bool {
	b := l.bus.Load()
	if b == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionProposeWait)
	defer cancel()
	_, err := (*b).Propose(ctx, oplog.SessionEntryOf("system", e))
	return err == nil
}

func (l *SessionLog) sweep() {
	ss := l.ss.Load()
	l.mu.Lock()
	ends := make([]oplog.SessionEntry, 0, len(l.ends))
	for _, e := range l.ends {
		ends = append(ends, e)
	}
	opens := make([]session, 0, len(l.opens))
	for _, se := range l.opens {
		opens = append(opens, se)
	}
	l.mu.Unlock()
	now := time.Now()
	for _, e := range ends {
		if !now.Before(time.UnixMilli(e.Expires)) || l.propose(e) {
			l.committed(func() { delete(l.ends, e.Hash); l.persistLocked() })
		}
	}
	for _, se := range opens {
		if ss == nil || !ss.has(se.hash) {
			l.committed(func() { delete(l.opens, se.hash); delete(l.tried, se.hash) })
			continue
		}
		if l.propose(entryOf(se, oplog.SessionOpen)) {
			l.committed(func() { delete(l.opens, se.hash); delete(l.tried, se.hash) })
			continue
		}
		l.mu.Lock()
		first := !l.tried[se.hash]
		l.tried[se.hash] = true
		l.mu.Unlock()
		if f := l.local.Load(); first && f != nil && ss.isLocal(se.hash) {
			(*f)(se)
		}
	}
}

// opened queues a new session's open: the sign-in never waits for it. If the
// first try fails the session is local, and the hook says so.
func (l *SessionLog) opened(se session) {
	l.committed(func() { l.opens[se.hash] = se })
	l.kick()
}

// endedSessions queues an end entry for each session; with wait it gives
// them up to sessionEndWait to commit, and what has not by then goes on in
// the background.
func (l *SessionLog) endedSessions(ended []session, wait bool) {
	if len(ended) == 0 {
		return
	}
	l.committed(func() {
		for _, se := range ended {
			delete(l.opens, se.hash) // an open not yet sent is never sent
			delete(l.tried, se.hash)
			l.ends[se.hash] = entryOf(se, oplog.SessionEnd)
		}
		l.persistLocked()
	})
	l.kick()
	if !wait {
		return
	}
	deadline := time.After(sessionEndWait)
	for {
		l.mu.Lock()
		owed := false
		for _, se := range ended {
			if _, ok := l.ends[se.hash]; ok {
				owed = true
			}
		}
		wake := l.wake
		l.mu.Unlock()
		if !owed {
			return
		}
		select {
		case <-wake:
		case <-deadline:
			return
		}
	}
}
