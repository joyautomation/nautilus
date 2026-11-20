package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/auth"
)

// The browser sign-in's cookie (#708 PR 5b): HttpOnly, SameSite=Strict,
// Path=/, host-only. It carries a random id and nothing else. Over TLS it
// is secureCookie, Secure and with the __Host- prefix: a browser accepts
// that name only from a secure page, with Path=/ and no Domain, so no
// plain-HTTP service on the same host can set or overwrite it. Over plain
// HTTP (loopback only) it is plainCookie, which any other web service on
// the same host is sent too, as with every cookie without the prefix.
const (
	secureCookie = "__Host-nautilus_session"
	plainCookie  = "nautilus_session"
)

// maxUserSessions is how many sessions one user may hold at once (a
// browser per screen in a control room is a handful); a sign-in past it
// ends that user's oldest.
const maxUserSessions = 32

// maxElevateFails is how many re-authentications in a row a session may
// fail before it is ended.
const maxElevateFails = 5

// session is one browser sign-in. The id itself is never kept: the store
// is keyed by its SHA-256, which is also what the journal records.
type session struct {
	hash string // hex SHA-256 of the id
	user string // lowercased user name: a local user's, or an OIDC name
	// oidc marks a sign-in at the OIDC provider (#708 PR 6): its user is
	// an OIDC name (principal oidc:<user>), proved by the account
	// (issuer, sub), which the name's pin must still name, with the group
	// ids its ID token carried. cred is empty: there is no password.
	oidc   bool
	issuer string
	sub    string
	groups []string
	created time.Time
	expires time.Time // absolute: created + auth.session, never renewed
	// authAt is when the user last proved their password in this session:
	// the sign-in, or the last re-authentication (POST /api/elevate). The
	// elevated permissions need it within auth.elevation.
	authAt time.Time
	// fails counts the re-authentications refused since the last that
	// passed: at maxElevateFails the session ends (a stolen cookie
	// guessing at the password).
	fails int
	// cred is the SHA-256 of the user's password hash it was proved
	// against: a session whose user's password changed (even in a change
	// that raced the sign-in, before its revocation could see the
	// session) acts no more.
	cred string
}

// principal is who the session acts as: user:<name>, or oidc:<name> with
// the group ids its sign-in carried.
func (se session) principal() auth.Principal {
	if se.oidc {
		return auth.OIDC(se.user, se.groups)
	}
	return auth.User(se.user)
}

// key is the session's principal as the journal names it: what a sign-out
// and the per-principal cap go by, so a local user and an OIDC name that
// read alike are never confused.
func (se session) key() string { return se.principal().String() }

// credPrint is a password hash's fingerprint, for session.cred.
func credPrint(phc string) string {
	sum := sha256.Sum256([]byte(phc))
	return hex.EncodeToString(sum[:])
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

// clock is the store's time now, its clock read under the lock (a test
// swaps it while streams re-check).
func (ss *sessions) clock() time.Time {
	ss.mu.Lock()
	now := ss.now
	ss.mu.Unlock()
	return now()
}

// randomID is 256 random bits, base64url: a session id, an OIDC state.
func randomID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on a supported platform
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
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

// open starts a session for a local user, proved by the password whose
// fingerprint is cred, and returns its id.
func (ss *sessions) open(user, cred string) (string, session) {
	return ss.start(session{user: user, cred: cred})
}

// start starts the session se describes (its user, and its proof: cred,
// or oidc with issuer, sub and groups) and returns its id (256 random
// bits, base64url), fresh at every sign-in.
func (ss *sessions) start(se session) (string, session) {
	id := randomID()
	now := ss.now()
	se.hash, se.created, se.expires, se.authAt = sessionHash(id), now, now.Add(ss.life), now
	key := se.key()

	ss.mu.Lock()
	var mine []*session
	for h, o := range ss.byHash {
		if !now.Before(o.expires) {
			delete(ss.byHash, h)
			continue
		}
		if o.key() == key {
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

// endAll ends every session and says how many there were.
func (ss *sessions) endAll() int {
	ss.mu.Lock()
	var ended []session
	for _, se := range ss.byHash {
		ended = append(ended, *se)
	}
	ss.byHash = map[string]*session{}
	ss.mu.Unlock()
	ss.told(ended)
	return len(ended)
}

// endPrincipal ends every session of the principal key names ("user:alice"
// disabled, its password changed, removed, or signed out; "oidc:x" signed
// out or unpinned) and says how many there were.
func (ss *sessions) endPrincipal(key string) int {
	ss.mu.Lock()
	var ended []session
	for h, se := range ss.byHash {
		if se.key() == key {
			delete(ss.byHash, h)
			ended = append(ended, *se)
		}
	}
	ss.mu.Unlock()
	ss.told(ended)
	return len(ended)
}

// elevated records a re-authentication of the session hash names, against
// the password whose fingerprint is cred: its authAt is now and its
// failures are forgotten. False when the session is gone, expired, or was
// proved against another password.
func (ss *sessions) elevated(hash, cred string) (session, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	se, ok := ss.byHash[hash]
	now := ss.now()
	if !ok || !now.Before(se.expires) || se.cred != cred {
		return session{}, false
	}
	se.authAt, se.fails = now, 0
	return *se, true
}

// elevateFailed counts a refused re-authentication of the session hash
// names. At maxElevateFails the session is ended, and ended says so.
func (ss *sessions) elevateFailed(hash string) (fails int, ended bool) {
	ss.mu.Lock()
	se, ok := ss.byHash[hash]
	if !ok {
		ss.mu.Unlock()
		return 0, false
	}
	se.fails++
	fails = se.fails
	if fails >= maxElevateFails {
		delete(ss.byHash, hash)
	}
	cp := *se
	ss.mu.Unlock()
	if fails >= maxElevateFails {
		ss.told([]session{cp})
		return fails, true
	}
	return fails, false
}
