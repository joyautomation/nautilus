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
	"github.com/joyautomation/nautilus/oplog"
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
	oidc    bool
	issuer  string
	sub     string
	groups  []string
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
	// local marks a session issued here that the operator log has not
	// committed (the log is read-only, or has not answered yet): it is valid
	// on this controller only, and becomes replicated if the log commits it
	// (#716). A session adopted from the log, or committed, is not local.
	local bool
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

	// rep, when set, carries sessions to the other controller of a pair
	// through the operator log (#716); nil on a lone controller.
	rep sessionReplicator
	// synced says whether the log's copy here is known complete. Without it
	// a session that came through the log is not honoured: the end of it may
	// be in a part of the log this copy lacks.
	synced func() bool
	// ended remembers the hashes the log ended, until they would have
	// expired, so an open that arrives after its end (a replay, a late
	// retry) does not bring the session back.
	ended map[string]time.Time
}

// sessionReplicator is what the store tells the log about.
type sessionReplicator interface {
	opened(session)
	endedSessions([]session)
}

func newSessions(life time.Duration) *sessions {
	return &sessions{now: time.Now, life: life, byHash: map[string]*session{}, ended: map[string]time.Time{}}
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
	rep := ss.rep
	ss.mu.Unlock()
	for _, se := range ended {
		for _, fn := range fns {
			fn(se)
		}
	}
	if rep != nil {
		rep.endedSessions(ended)
	}
}

// toldReplicated is told for sessions the log ended: the streams close, and
// nothing is proposed back.
func (ss *sessions) toldReplicated(ended []session) {
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
	se.hash, se.created, se.expires = sessionHash(id), now, now.Add(ss.life)
	// A password was proved just now. An OIDC sign-in proves only what
	// the provider says: when the person last entered credentials there
	// (auth_time, given in se.authAt; zero when it did not say), never
	// later than now. A sign-in that rode on an old session at the
	// provider is not fresh.
	if !se.oidc || se.authAt.After(now) {
		se.authAt = now
	}
	key := se.key()

	ss.mu.Lock()
	se.local = ss.rep != nil
	for h, until := range ss.ended {
		if !now.Before(until) {
			delete(ss.ended, h)
		}
	}
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
	held := se // the store's own copy: the log may change it while the caller reads se
	ss.byHash[se.hash] = &held
	rep := ss.rep
	ss.mu.Unlock()
	ss.told(ended)
	if rep != nil {
		rep.opened(se)
	}
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
	if !ss.honoured(se) {
		return session{}, false
	}
	return *se, true
}

// honoured: a session that came through the log (not local) counts only
// while this copy of the log is known complete. Called with ss.mu held.
func (ss *sessions) honoured(se *session) bool {
	return ss.rep == nil || se.local || ss.synced == nil || ss.synced()
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

// endAll ends every session and says how many there were. In a pair
// (rep set) only the local ones, which the log never carried: the others
// are the pair's, valid on whichever controller leads, and are ended by
// the log's own end entries (#716).
func (ss *sessions) endAll() int {
	ss.mu.Lock()
	var ended []session
	for h, se := range ss.byHash {
		if ss.rep != nil && !se.local {
			continue
		}
		ended = append(ended, *se)
		delete(ss.byHash, h)
	}
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

// byHashLive is the live session hash names. An expired one is dropped.
func (ss *sessions) byHashLive(hash string) (session, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	se, ok := ss.byHash[hash]
	if !ok {
		return session{}, false
	}
	if !ss.now().Before(se.expires) {
		delete(ss.byHash, hash)
		return session{}, false
	}
	if !ss.honoured(se) {
		return session{}, false
	}
	return *se, true
}

// elevatedOIDC records a confirmation at the OIDC provider of the session
// hash names (#708 PR 6b): its authAt is authAt (when the person entered
// credentials there), never moved back, and its failures are forgotten.
// False when the session is gone, expired, not an OIDC one, or proved by
// another account than (issuer, sub).
func (ss *sessions) elevatedOIDC(hash, issuer, sub string, authAt time.Time) (session, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	se, ok := ss.byHash[hash]
	now := ss.now()
	if !ok || !now.Before(se.expires) || !se.oidc || se.issuer != issuer || se.sub != sub {
		return session{}, false
	}
	if authAt.After(now) {
		authAt = now
	}
	if authAt.After(se.authAt) {
		se.authAt = authAt
	}
	se.fails = 0
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

// adopt takes a session the log opened (on the other controller, or
// replayed): its hash, who it is, when it ends. Never an elevation:
// authAt stays zero, so an elevated action asks for the password again.
// An end the log has already carried, or an expiry, wins.
func (ss *sessions) adopt(e oplog.SessionEntry) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	expires := time.UnixMilli(e.Expires)
	if !ss.now().Before(expires) {
		return
	}
	if _, gone := ss.ended[e.Hash]; gone {
		return
	}
	if se, ok := ss.byHash[e.Hash]; ok {
		se.local = false // the log has it now
		return
	}
	ss.byHash[e.Hash] = &session{hash: e.Hash, user: e.User, oidc: e.OIDC, issuer: e.Issuer, sub: e.Sub, groups: e.Groups,
		created: time.UnixMilli(e.Created), expires: expires, cred: e.Cred}
}

// endByLog ends the session the log ended and remembers its hash until it
// would have expired.
func (ss *sessions) endByLog(e oplog.SessionEntry) {
	ss.mu.Lock()
	until := time.UnixMilli(e.Expires)
	if ss.now().Before(until) {
		ss.ended[e.Hash] = until
	}
	se, ok := ss.byHash[e.Hash]
	if ok {
		delete(ss.byHash, e.Hash)
	}
	ss.mu.Unlock()
	if ok {
		ss.toldReplicated([]session{*se})
	}
}

// has says whether the session is still held (not expired, not ended).
func (ss *sessions) has(hash string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	se, ok := ss.byHash[hash]
	return ok && ss.now().Before(se.expires)
}

// isLocal says whether the session is held and still local.
func (ss *sessions) isLocal(hash string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	se, ok := ss.byHash[hash]
	return ok && se.local
}
