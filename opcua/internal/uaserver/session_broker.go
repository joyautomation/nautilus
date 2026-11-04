package uaserver

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/uasc"
)

// authTokenLength is the size of a session's authentication token, in bytes. The token is the
// session's only credential once it is activated (Part 4 §5.6.2), so it comes from crypto/rand
// and is long enough not to be guessed.
const authTokenLength = 32

// maxPublishRequests is how many Publish requests a session may have queued, unless it owns as
// many subscriptions or more: then it may queue one more than it has subscriptions, since "a
// Server shall accept more queued Publish requests than created Subscriptions" (Part 4
// §5.14.5.1; the OPC Foundation .NET stack's limit, so no client is refused subscriptions to
// keep the queue short). A new request past the limit de-queues the oldest, which is answered
// with Bad_TooManyPublishRequests.
const maxPublishRequests = 100

type session struct {
	cfg sessionConfig

	ID                *ua.NodeID
	AuthTokenID       *ua.NodeID
	serverNonce       []byte
	remoteCertificate []byte

	// pubMu guards the Publish queue and the count of subscriptions the session owns, so that
	// whether a Publish is queued and whether the last subscription has gone are decided one
	// at a time: a request is never queued on a session that has just lost its last
	// subscription, where nothing would answer it.
	pubMu sync.Mutex
	// pubReqs are the Publish requests waiting for a subscription to answer them, oldest first.
	pubReqs []PubReq
	// subscriptions is how many subscriptions the session owns.
	subscriptions int
	// pubReady holds a token while pubReqs may be non-empty, for a subscription's task to
	// wait on: whoever queues a request, or takes one and leaves others, puts one there. A
	// token with nothing queued behind it is harmless (takePublish reports nothing).
	pubReady chan struct{}

	// mu guards activated and notices. Every request is handled on the server's one message loop, so this
	// is defensive; channel is set at creation and never changes.
	mu sync.Mutex
	// channel is the secure channel the session was created on. ActivateSession must arrive on
	// it, and every later request too: a token presented on another channel is refused, so a
	// token seen on the wire cannot be replayed from a second connection. Moving a session to
	// another channel (Part 4 §5.6.3, "transfer") is not supported; a client that lost its
	// channel creates a new session.
	channel *uasc.SecureChannel
	// identity is who ActivateSession's user identity token says the user is:
	// "anonymous", the only kind advertised (Part 4 §5.6.3).
	identity string
	// secured is set when the session was created on a channel that signs (Sign or
	// SignAndEncrypt under a policy other than None), and applicationURI is then the client's
	// ApplicationUri, which CreateSession has held to the URI in its certificate. Under None
	// the client's ApplicationUri is only what it says, and applicationURI is empty. Both are
	// set on the message loop at CreateSession and never change.
	secured        bool
	applicationURI string
	// activated is set by the first successful ActivateSession. Until then the session can be
	// activated or closed and nothing else.
	activated bool
	// notices are the subscriptions (id to the notice's sequence number) moved to another
	// session that this one has not been told of yet: the next Publish it sends is answered
	// with one (SubscriptionService.noticeFor). Guarded by mu.
	notices map[uint32]uint32
}

// mayTransfer is whether a subscription of the session from may be transferred to the session
// to: the TransferSubscriptions rule of Part 4 §5.14.7. Both must act for the same user. For an
// anonymous user that means the same client application, which only a signed channel can
// show: both sessions must have been created on one, with the same ApplicationUri (the one
// their certificates carry). A session on None cannot take another's subscription, nor have
// its own taken. Bad_UserAccessDenied otherwise. Sessions are read on the message loop, where
// their identity and security are written.
func mayTransfer(from, to *session) ua.StatusCode {
	if from == nil || to == nil {
		return ua.StatusBadUserAccessDenied
	}
	if from.identity != to.identity {
		return ua.StatusBadUserAccessDenied
	}
	if from.identity == "anonymous" || from.identity == "" {
		if !from.secured || !to.secured || from.applicationURI == "" || from.applicationURI != to.applicationURI {
			return ua.StatusBadUserAccessDenied
		}
	}
	return ua.StatusOK
}

// keepNotice keeps a notice that subscription id has moved, for the session's next Publish.
func (s *session) keepNotice(id, seq uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notices == nil {
		s.notices = map[uint32]uint32{}
	}
	s.notices[id] = seq
}

// dropNotice forgets a kept notice about subscription id.
func (s *session) dropNotice(id uint32) {
	s.mu.Lock()
	delete(s.notices, id)
	s.mu.Unlock()
}

// takeNotice removes and returns one kept notice, the lowest subscription id first.
func (s *session) takeNotice() (id, seq uint32, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.notices {
		if !ok || k < id {
			id, ok = k, true
		}
	}
	if ok {
		seq = s.notices[id]
		delete(s.notices, id)
	}
	return id, seq, ok
}

// queuePublish queues pr for the session's subscriptions to answer, and returns the requests
// de-queued to make room for it, oldest first, which are owed Bad_TooManyPublishRequests (Part
// 4 §5.14.5.1). ok is false, and pr is not queued, when the session owns no subscription: the
// request is owed Bad_NoSubscription (§5.14.5.3).
func (s *session) queuePublish(pr PubReq) (dequeued []PubReq, ok bool) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.subscriptions == 0 {
		return nil, false
	}
	// More than one only when the session has lost subscriptions since its queue filled.
	for limit := max(maxPublishRequests, s.subscriptions+1); len(s.pubReqs) >= limit; {
		dequeued = append(dequeued, s.pubReqs[0])
		s.pubReqs[0] = PubReq{}
		s.pubReqs = s.pubReqs[1:]
	}
	s.pubReqs = append(s.pubReqs, pr)
	s.signalPublish()
	return dequeued, true
}

// takePublish de-queues the oldest Publish request, if there is one.
func (s *session) takePublish() (PubReq, bool) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if len(s.pubReqs) == 0 {
		return PubReq{}, false
	}
	pr := s.pubReqs[0]
	s.pubReqs[0] = PubReq{}
	s.pubReqs = s.pubReqs[1:]
	if len(s.pubReqs) > 0 {
		s.signalPublish() // another task may be waiting for the next one
	}
	return pr, true
}

// signalPublish leaves a token on pubReady, if there is none. Called with pubMu held.
func (s *session) signalPublish() {
	select {
	case s.pubReady <- struct{}{}:
	default:
	}
}

// publishQueued is a channel that has a token once a Publish request may be queued; the
// waiter then calls takePublish, which may find another task took it first.
func (s *session) publishQueued() <-chan struct{} {
	return s.pubReady
}

// queuedPublishes is how many Publish requests are waiting.
func (s *session) queuedPublishes() int {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	return len(s.pubReqs)
}

// subscriptionAdded counts a subscription the session has come to own: created, or
// transferred to it.
func (s *session) subscriptionAdded() {
	s.pubMu.Lock()
	s.subscriptions++
	s.pubMu.Unlock()
}

// subscriptionGone counts a subscription the session owns no more: deleted, or transferred
// away. Its queued requests stay; orphanedPublishes takes them once there is no subscription
// left to answer them.
func (s *session) subscriptionGone() {
	s.pubMu.Lock()
	if s.subscriptions > 0 {
		s.subscriptions--
	}
	s.pubMu.Unlock()
}

// orphanedPublishes de-queues and returns every queued Publish request when the session owns
// no subscription, which are owed Bad_NoSubscription: "If this is the last Subscription for
// the Session, then all Publish requests still queued for that Session are de-queued and
// shall be returned with Bad_NoSubscription" (Part 4 §5.14.8.1). It returns nothing while
// the session owns one.
func (s *session) orphanedPublishes() []PubReq {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.subscriptions > 0 || len(s.pubReqs) == 0 {
		return nil
	}
	out := s.pubReqs
	s.pubReqs = nil
	return out
}

// boundTo reports whether sc is the channel the session was created on.
func (s *session) boundTo(sc *uasc.SecureChannel) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.channel == sc
}

func (s *session) activate() {
	s.mu.Lock()
	s.activated = true
	s.mu.Unlock()
}

func (s *session) isActivated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activated
}

type sessionConfig struct {
	sessionTimeout time.Duration
}

type sessionBroker struct {
	// mu protects concurrent modification of s
	mu sync.Mutex

	// s contains all sessions watched by the session broker
	s      map[string]*session
	logger Logger
}

func newSessionBroker(logger Logger) *sessionBroker {
	return &sessionBroker{
		s:      make(map[string]*session),
		logger: logger,
	}
}

// NewSession makes a session bound to sc, with a fresh random authentication token.
func (sb *sessionBroker) NewSession(sc *uasc.SecureChannel) (*session, error) {
	tok := make([]byte, authTokenLength)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	s := &session{
		ID:          ua.NewGUIDNodeID(1, uuid.New().String()),
		AuthTokenID: ua.NewByteStringNodeID(0, tok),
		pubReady:    make(chan struct{}, 1),
		channel:     sc,
	}

	sb.mu.Lock()
	sb.s[s.AuthTokenID.String()] = s
	sb.mu.Unlock()

	return s, nil
}

// CloseChannel removes every session bound to sc and returns them. A session cannot move to
// another channel, so once its channel is gone nothing can use it again; without this, each
// reconnect of a client left one dead session (and its channel) behind for good.
func (sb *sessionBroker) CloseChannel(sc *uasc.SecureChannel) []*session {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	var out []*session
	for k, s := range sb.s {
		if s.boundTo(sc) {
			out = append(out, s)
			delete(sb.s, k)
		}
	}
	return out
}

func (sb *sessionBroker) Close(authToken *ua.NodeID) error {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if sb.s[authToken.String()] == nil {
		if sb.logger != nil {
			sb.logger.Warn("sessionBroker.Close: error looking up session %v", authToken)
		}
	}
	delete(sb.s, authToken.String())

	return nil
}

func (sb *sessionBroker) Session(authToken *ua.NodeID) *session {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	s := sb.s[authToken.String()]
	if s == nil {
		if sb.logger != nil {
			sb.logger.Warn("sessionBroker.Session: error looking up session %v", authToken)
		}
	}

	return s
}
