package uaserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/binary"
	"log"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uapolicy"
	"github.com/joyautomation/nautilus/opcua/internal/uasc"
)

const (
	sessionTimeoutMin     = 100 * time.Millisecond
	sessionTimeoutMax     = 1 * time.Hour
	sessionTimeoutDefault = 60 * time.Second

	sessionNonceLength = 32
)

// SessionService implements the Session Service Set.
//
// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6
type SessionService struct {
	srv *Server
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6.2
func (s *SessionService) CreateSession(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.CreateSessionRequest](r)
	if err != nil {
		return nil, err
	}

	if code := checkClientCertificate(sc, req); code != ua.StatusOK {
		return nil, code
	}

	// New session, bound to the channel that asked for it.
	sess, err := s.srv.sb.NewSession(sc)
	if err != nil {
		log.Printf("error creating session authentication token")
		return nil, ua.StatusBadInternalError
	}

	// Ensure session timeout is reasonable
	sess.cfg.sessionTimeout = time.Duration(req.RequestedSessionTimeout) * time.Millisecond
	if sess.cfg.sessionTimeout > sessionTimeoutMax || sess.cfg.sessionTimeout < sessionTimeoutMin {
		sess.cfg.sessionTimeout = sessionTimeoutDefault
	}

	nonce := make([]byte, sessionNonceLength)
	if _, err := rand.Read(nonce); err != nil {
		log.Printf("error creating session nonce")
		return nil, ua.StatusBadInternalError
	}
	sess.serverNonce = nonce
	sess.remoteCertificate = req.ClientCertificate
	// checkClientCertificate held a signed channel's ApplicationUri to the certificate's, so
	// it names the client application; TransferSubscriptions relies on that (mayTransfer).
	if signs(sc) {
		sess.secured = true
		if req.ClientDescription != nil {
			sess.applicationURI = req.ClientDescription.ApplicationURI
		}
	}

	sig, alg, err := sc.NewSessionSignature(req.ClientCertificate, req.ClientNonce)
	if err != nil {
		log.Printf("error creating session signature")
		return nil, ua.StatusBadInternalError
	}

	// Part 4 §5.6.2: the endpoints available for the server, which the
	// client checks against what GetEndpoints told it. endpointUrl only
	// helps choose which URLs to return; a client that dialled an address
	// the server does not know (NAT, an alias) must still get a list, or
	// it refuses the session.
	matching_endpoints := s.srv.endpointsFor(req.EndpointURL)

	response := &ua.CreateSessionResponse{
		ResponseHeader:        responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
		SessionID:             sess.ID,
		AuthenticationToken:   sess.AuthTokenID,
		RevisedSessionTimeout: float64(sess.cfg.sessionTimeout / time.Millisecond),
		MaxRequestMessageSize: 0, // Not used
		ServerSignature: &ua.SignatureData{
			Signature: sig,
			Algorithm: alg,
		},
		ServerCertificate: s.srv.cfg.certificate,
		ServerNonce:       nonce,
		ServerEndpoints:   matching_endpoints,
	}

	return response, nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6.3
func (s *SessionService) ActivateSession(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.ActivateSessionRequest](r)
	if err != nil {
		return nil, err
	}

	sess := s.srv.sb.Session(req.RequestHeader.AuthenticationToken)
	if sess == nil {
		return nil, ua.StatusBadSessionIDInvalid
	}
	// Part 4 §5.6.3: the first activation must come on the channel that created the session.
	// A later one may move the session to another channel when the client proves it is the
	// same application; this server does not support that move (a client that lost its
	// channel creates a new session), so every activation stays on the creating channel.
	if !sess.boundTo(sc) {
		return nil, ua.StatusBadSecureChannelIDInvalid
	}

	// One activation at a time: a UserName check under way off the message
	// loop is bound to the server nonce it read.
	sess.mu.Lock()
	busy := sess.activating
	sess.mu.Unlock()
	if busy {
		return nil, ua.StatusBadInvalidState
	}

	if tok, ok := userNameToken(req.UserIdentityToken); ok && s.srv.cfg.userName != nil {
		return s.activateUser(sc, sess, req, reqID, tok)
	}

	identity, code := s.srv.checkIdentity(req.UserIdentityToken)
	if code != ua.StatusOK {
		return nil, code
	}

	err = sc.VerifySessionSignature(sess.remoteCertificate, sess.serverNonce, req.ClientSignature.Signature)
	if err != nil {
		if s.srv.cfg.logger != nil {
			s.srv.cfg.logger.Warn("error verifying session signature with nonce: %s", err)
		}
		return nil, ua.StatusBadSecurityChecksFailed
	}

	nonce := make([]byte, sessionNonceLength)
	if _, err := rand.Read(nonce); err != nil {
		log.Printf("error creating session nonce")
		return nil, ua.StatusBadInternalError
	}
	sess.mu.Lock()
	sess.serverNonce = nonce
	sess.identity, sess.cred = identity, ""
	sess.mu.Unlock()
	sess.activate()

	response := &ua.ActivateSessionResponse{
		ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
		ServerNonce:    nonce,
		// Results:         []ua.StatusCode{},
		// DiagnosticInfos: []*ua.DiagnosticInfo{},
	}

	return response, nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6.4
func (s *SessionService) CloseSession(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.CloseSessionRequest](r)
	if err != nil {
		return nil, err
	}

	sess := s.srv.sb.Session(req.RequestHeader.AuthenticationToken)

	err = s.srv.sb.Close(req.RequestHeader.AuthenticationToken)
	if err != nil {
		return nil, ua.StatusBadSessionIDInvalid
	}

	// Part 4 §5.6.4: deleteSubscriptions TRUE deletes every subscription the
	// session owns, with its monitored items, before the response; FALSE
	// leaves them to run until their own lifetime expires (nothing publishes
	// for them once the session is gone, so the lifetime counter does it).
	if req.DeleteSubscriptions && sess != nil && s.srv.SubscriptionService != nil {
		s.srv.SubscriptionService.DeleteSessionSubscriptions(sess)
	}

	response := &ua.CloseSessionResponse{
		ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
	}

	return response, nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6.5
func (s *SessionService) Cancel(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.CancelRequest](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}

// checkClientCertificate holds CreateSession on a secured channel to the
// channel's own client: Part 4 §5.6.2 has the server check that the
// clientCertificate is the one the SecureChannel was opened with
// (Bad_SecurityChecksFailed otherwise), and that the clientDescription's
// applicationUri is the URI in that certificate (Bad_CertificateUriInvalid).
// Under None there is no channel certificate to hold it to.
func checkClientCertificate(sc *uasc.SecureChannel, req *ua.CreateSessionRequest) ua.StatusCode {
	if sc.SecurityPolicyURI() == ua.SecurityPolicyURINone || sc.SecurityPolicyURI() == "" {
		return ua.StatusOK
	}
	// Either may carry the client's issuers after its own certificate; the
	// leaves are what must match.
	sent, err := x509.ParseCertificates(req.ClientCertificate)
	if err != nil || len(sent) == 0 {
		return ua.StatusBadSecurityChecksFailed
	}
	chan_, err := x509.ParseCertificates(sc.RemoteCertificate())
	if err != nil || len(chan_) == 0 || !bytes.Equal(sent[0].Raw, chan_[0].Raw) {
		return ua.StatusBadSecurityChecksFailed
	}
	cert := sent[0]
	uri := ""
	if req.ClientDescription != nil {
		uri = req.ClientDescription.ApplicationURI
	}
	for _, u := range cert.URIs {
		if u.String() == uri {
			return ua.StatusOK
		}
	}
	return ua.StatusBadCertificateURIInvalid
}

// signs reports whether a channel signs its messages: a policy other than None, in Sign or
// SignAndEncrypt.
func signs(sc *uasc.SecureChannel) bool {
	if p := sc.SecurityPolicyURI(); p == "" || p == ua.SecurityPolicyURINone {
		return false
	}
	m := sc.SecurityMode()
	return m == ua.MessageSecurityModeSign || m == ua.MessageSecurityModeSignAndEncrypt
}

// checkIdentity reads ActivateSession's user identity token. Only anonymous
// is advertised, so only an AnonymousIdentityToken is accepted, with a
// policyId one an endpoint offers; a null token counts as anonymous (Part 4
// §5.6.3), and so does an empty policyId, which is what gopcua's own client
// sends. Anything else is Bad_IdentityTokenInvalid; gopcua's server never
// looked. It returns the identity recorded on the session.
func (s *Server) checkIdentity(tok *ua.ExtensionObject) (string, ua.StatusCode) {
	if nullExtensionObject(tok) {
		return "anonymous", ua.StatusOK // a null token (Part 4 §5.6.3)
	}
	anon, ok := tok.Value.(*ua.AnonymousIdentityToken)
	if !ok {
		return "", ua.StatusBadIdentityTokenInvalid
	}
	if anon.PolicyID == "" {
		return "anonymous", ua.StatusOK
	}
	for _, ep := range s.Endpoints() {
		for _, p := range ep.UserIdentityTokens {
			if p.TokenType == ua.UserTokenTypeAnonymous && p.PolicyID == anon.PolicyID {
				return "anonymous", ua.StatusOK
			}
		}
	}
	return "", ua.StatusBadIdentityTokenInvalid
}

// userNameCheckWait bounds a UserName check: past it the activation is
// refused, so a client waiting on a busy password hash is answered.
var userNameCheckWait = 15 * time.Second

// userNameToken is ActivateSession's token when it is a UserName one.
func userNameToken(tok *ua.ExtensionObject) (*ua.UserNameIdentityToken, bool) {
	if nullExtensionObject(tok) {
		return nil, false
	}
	t, ok := tok.Value.(*ua.UserNameIdentityToken)
	return t, ok && t != nil
}

// activateUser activates sess with a UserName token. The session signature
// is verified first, on the message loop, so a client must hold the
// session's certificate before a password costs a hash; the token is
// decrypted and its nonce checked there too. The check itself (a password
// hash, perhaps a wait for a slot) runs off the loop, and its answer is
// sent from there: the handler answers nothing now (nil, nil), as Publish
// does.
func (s *SessionService) activateUser(sc *uasc.SecureChannel, sess *session, req *ua.ActivateSessionRequest, reqID uint32, tok *ua.UserNameIdentityToken) (ua.Response, error) {
	if err := sc.VerifySessionSignature(sess.remoteCertificate, sess.serverNonce, req.ClientSignature.Signature); err != nil {
		return nil, ua.StatusBadSecurityChecksFailed
	}
	name, password, code := s.srv.userNamePassword(sc, tok, sess.serverNonce)
	if code != ua.StatusOK {
		return nil, code
	}
	sess.mu.Lock()
	sess.activating = true
	sess.mu.Unlock()
	check := s.srv.cfg.userName
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), userNameCheckWait)
		identity, cred, code := check(ctx, sc, name, password)
		cancel()
		clear(password)
		var resp ua.Response
		sess.mu.Lock()
		sess.activating = false
		// The session may have been closed, or its channel lost, meanwhile.
		if code == ua.StatusOK && s.srv.sb.Session(req.RequestHeader.AuthenticationToken) != sess {
			code = ua.StatusBadSessionIDInvalid
		}
		if code == ua.StatusOK {
			nonce := make([]byte, sessionNonceLength)
			if _, err := rand.Read(nonce); err != nil {
				code = ua.StatusBadInternalError
			} else {
				sess.serverNonce = nonce
				sess.identity, sess.cred = identity, cred
				resp = &ua.ActivateSessionResponse{
					ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
					ServerNonce:    nonce,
				}
			}
		}
		sess.mu.Unlock()
		if code != ua.StatusOK {
			resp = &ua.ServiceFault{ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, code)}
		} else {
			sess.activate()
		}
		if err := sc.SendResponseWithContext(context.Background(), reqID, resp); err != nil && s.srv.cfg.logger != nil {
			s.srv.cfg.logger.Warn("error sending an ActivateSession response: %s", err)
		}
	}()
	return nil, nil
}

// userNamePassword decrypts a UserName token's password (Part 4 §7.41,
// the legacy secret: a little-endian length, the password, the server
// nonce). It is accepted only on a channel that signs, under one of the
// UserName token policies the endpoints advertise, encrypted with that
// policy's asymmetric algorithm, and ending in sess's current server nonce
// (a token replayed from an earlier activation carries an older one).
func (s *Server) userNamePassword(sc *uasc.SecureChannel, tok *ua.UserNameIdentityToken, nonce []byte) (string, []byte, ua.StatusCode) {
	if !signs(sc) {
		return "", nil, ua.StatusBadIdentityTokenRejected
	}
	policy, found := "", false
	for _, ep := range s.Endpoints() {
		for _, p := range ep.UserIdentityTokens {
			if p.TokenType == ua.UserTokenTypeUserName && p.PolicyID == tok.PolicyID {
				policy, found = p.SecurityPolicyURI, true
			}
		}
	}
	if !found {
		return "", nil, ua.StatusBadIdentityTokenInvalid
	}
	if policy == "" {
		policy = sc.SecurityPolicyURI()
	}
	if policy == "" || policy == ua.SecurityPolicyURINone || s.cfg.privateKey == nil {
		return "", nil, ua.StatusBadIdentityTokenInvalid
	}
	enc, err := uapolicy.Asymmetric(policy, s.cfg.privateKey, nil)
	if err != nil || tok.EncryptionAlgorithm != enc.EncryptionURI() {
		return "", nil, ua.StatusBadIdentityTokenInvalid
	}
	plain, err := enc.Decrypt(tok.Password)
	if err != nil {
		return "", nil, ua.StatusBadIdentityTokenInvalid
	}
	pw, ok := passwordSecret(plain, nonce)
	clear(plain)
	if !ok {
		return "", nil, ua.StatusBadIdentityTokenInvalid
	}
	return tok.UserName, pw, ua.StatusOK
}

// passwordSecret reads a decrypted UserName secret: a little-endian
// length, then that many bytes, the password followed by nonce. It returns
// a copy of the password, or false when the secret is malformed or does
// not end in nonce (an empty nonce never matches).
func passwordSecret(plain, nonce []byte) ([]byte, bool) {
	if len(plain) < 4 || len(nonce) == 0 {
		return nil, false
	}
	n := binary.LittleEndian.Uint32(plain)
	body := plain[4:]
	if uint64(n) > uint64(len(body)) || uint64(n) < uint64(len(nonce)) {
		return nil, false
	}
	secret := body[:n]
	cut := len(secret) - len(nonce)
	if subtle.ConstantTimeCompare(secret[cut:], nonce) != 1 {
		return nil, false
	}
	return bytes.Clone(secret[:cut]), true
}

// nullExtensionObject reports an extension object with the null type id:
// "no token", which Part 4 §5.6.3 treats as anonymous. One with a type but
// no decoded body is not null.
func nullExtensionObject(e *ua.ExtensionObject) bool {
	if e == nil || e.TypeID == nil || e.TypeID.NodeID == nil {
		return true
	}
	n := e.TypeID.NodeID
	return n.Namespace() == 0 && n.Type() != ua.NodeIDTypeString && n.Type() != ua.NodeIDTypeGUID && n.Type() != ua.NodeIDTypeByteString && n.IntID() == 0
}
