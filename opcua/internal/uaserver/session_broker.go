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

type session struct {
	cfg sessionConfig

	ID                *ua.NodeID
	AuthTokenID       *ua.NodeID
	serverNonce       []byte
	remoteCertificate []byte

	PublishRequests chan PubReq

	// mu guards activated. Every request is handled on the server's one message loop, so this
	// is defensive; channel is set at creation and never changes.
	mu sync.Mutex
	// channel is the secure channel the session was created on. ActivateSession must arrive on
	// it, and every later request too: a token presented on another channel is refused, so a
	// token seen on the wire cannot be replayed from a second connection. Moving a session to
	// another channel (Part 4 §5.6.3, "transfer") is not supported; a client that lost its
	// channel creates a new session.
	channel *uasc.SecureChannel
	// activated is set by the first successful ActivateSession. Until then the session can be
	// activated or closed and nothing else.
	activated bool
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
		ID:              ua.NewGUIDNodeID(1, uuid.New().String()),
		AuthTokenID:     ua.NewByteStringNodeID(0, tok),
		PublishRequests: make(chan PubReq, 100),
		channel:         sc,
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
