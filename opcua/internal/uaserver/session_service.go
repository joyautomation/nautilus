package uaserver

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"log"
	"time"

	"github.com/gopcua/opcua/ua"
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
	sess.serverNonce = nonce
	sess.identity = identity
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
