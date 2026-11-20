package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// sessionCookie is the browser sign-in's cookie (#708 PR 5b): HttpOnly,
// SameSite=Strict, Path=/, host-only, Secure when the request reached the
// controller over TLS. It carries a random id and nothing else.
const sessionCookie = "nautilus_session"

// maxUserSessions is how many sessions one user may hold at once (a
// browser per screen in a control room is a handful); a sign-in past it
// ends that user's oldest.
const maxUserSessions = 32

// session is one browser sign-in. The id itself is never kept: the store
// is keyed by its SHA-256, which is also what the journal records.
type session struct {
	hash    string // hex SHA-256 of the id
	user    string // lowercased user name
	created time.Time
	expires time.Time // absolute: created + auth.session, never renewed
	// authAt is when the user last proved their password in this session
	// (sign-in today; a re-authentication with 5c's elevation).
	authAt time.Time
}

// sessions is the controller's sign-ins, in memory: a restart signs
// everyone out, and on a redundant pair they live on the box that issued
// them, the leader (decision (c) on #708; a standby refers a browser to
// the leader).
type sessions struct {
	now  func() time.Time
	life time.Duration

	mu     sync.Mutex
	byHash map[string]*session
	onEnd  []func(session)
}

func newSessions(life time.Duration) *sessions {
	return &sessions{now: time.Now, life: life, byHash: map[string]*session{}}
}

// sessionHash is the SHA-256 of a session id, hex: the store's key and the
// journal's "session".
func sessionHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// OnEnd adds what is told when a session is ended before its expiry (a
// sign-out, a revocation): 5b-2 closes the user's open live streams from
// it.
func (ss *sessions) OnEnd(fn func(session)) {
	ss.mu.Lock()
	ss.onEnd = append(ss.onEnd, fn)
	ss.mu.Unlock()
}

func (ss *sessions) told(ended []session) {
	if len(ended) == 0 {
		return
	}
	ss.mu.Lock()
	fns := append([]func(session){}, ss.onEnd...)
	ss.mu.Unlock()
	for _, se := range ended {
		for _, fn := range fns {
			fn(se)
		}
	}
}

// open starts a session for user and returns its id (256 random bits,
// base64url), fresh at every sign-in.
func (ss *sessions) open(user string) (string, session) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on a supported platform
	}
	id := base64.RawURLEncoding.EncodeToString(b[:])
	now := ss.now()
	se := session{hash: sessionHash(id), user: user, created: now, expires: now.Add(ss.life), authAt: now}

	ss.mu.Lock()
	var mine []*session
	for h, o := range ss.byHash {
		if !now.Before(o.expires) {
			delete(ss.byHash, h)
			continue
		}
		if o.user == user {
			mine = append(mine, o)
		}
	}
	var ended []session
	if len(mine) >= maxUserSessions {
		sort.Slice(mine, func(i, j int) bool { return mine[i].created.Before(mine[j].created) })
		for _, o := range mine[:len(mine)-maxUserSessions+1] {
			delete(ss.byHash, o.hash)
			ended = append(ended, *o)
		}
	}
	ss.byHash[se.hash] = &se
	ss.mu.Unlock()
	ss.told(ended)
	return id, se
}

// lookup is the live session whose id this is. An expired one is
// dropped.
func (ss *sessions) lookup(id string) (session, bool) {
	if id == "" {
		return session{}, false
	}
	h := sessionHash(id)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	se, ok := ss.byHash[h]
	if !ok {
		return session{}, false
	}
	if !ss.now().Before(se.expires) {
		delete(ss.byHash, h)
		return session{}, false
	}
	return *se, true
}

// end ends the session whose id this is (a sign-out).
func (ss *sessions) end(id string) (session, bool) {
	h := sessionHash(id)
	ss.mu.Lock()
	se, ok := ss.byHash[h]
	if ok {
		delete(ss.byHash, h)
	}
	ss.mu.Unlock()
	if !ok {
		return session{}, false
	}
	ss.told([]session{*se})
	return *se, true
}

// endUser ends every session user holds (disabled, password changed,
// removed) and says how many there were.
func (ss *sessions) endUser(user string) int {
	ss.mu.Lock()
	var ended []session
	for h, se := range ss.byHash {
		if se.user == user {
			delete(ss.byHash, h)
			ended = append(ended, *se)
		}
	}
	ss.mu.Unlock()
	ss.told(ended)
	return len(ended)
}
