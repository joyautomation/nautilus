// client.go opens one session on one server: discover the endpoints, pick
// the one the project asked for (or the most secure offered), present the
// certificate the policy requires, authenticate, connect. Shared by the
// driver (Start's reconnect loop) and the CLI (`naut opcua import|browse`).
//
// Reconnection is OURS, not gopcua's: AutoReconnect is off, a transport
// failure closes the client and the driver dials again with backoff — the
// same ladder modbus climbs. The library's auto-reconnect was the one area
// with open correctness issues when this was written (publish loops staying
// paused, a Connected state reported after a severed link), and a driver
// that owns the ladder also owns the quality verdicts that go with it.
package opcua

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// Security names the endpoint to use. Policy is one of None Basic128Rsa15
// Basic256 Basic256Sha256 Aes128_Sha256_RsaOaep Aes256_Sha256_RsaPss (case
// does not matter); Mode is None, Sign or SignAndEncrypt. An empty Policy
// takes the most secure endpoint the server offers, an empty Mode the
// strongest mode of that policy.
type Security struct {
	Policy string
	Mode   string
}

// Connect is everything a dial needs besides the endpoint URL.
type Connect struct {
	Security Security
	// Username/Password select the UserName token; empty is Anonymous.
	Username string
	Password string
	// PKIDir holds (or receives) the client certificate; needed for any
	// policy but None.
	PKIDir string
	// ApplicationURI is announced in the session and must match the
	// certificate's URI SAN. Empty is DefaultApplicationURI.
	ApplicationURI string
	// Timeout bounds the dial and every request (default 5s).
	Timeout time.Duration
	// SessionName is what the server shows for this session.
	SessionName string
}

// Session is a connected client with the endpoint it settled on.
type Session struct {
	Client   *opcua.Client
	Endpoint *ua.EndpointDescription
}

// Dial discovers, selects, and connects. The endpoint URL given is the one
// dialled even when the server advertises another (a server behind NAT or
// a tailnet advertises its own idea of its address, which the client may
// not be able to reach — tentacle learned this against Ignition).
func Dial(ctx context.Context, endpoint string, c Connect) (*Session, error) {
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.ApplicationURI == "" {
		c.ApplicationURI = DefaultApplicationURI
	}
	dctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	eps, err := opcua.GetEndpoints(dctx, endpoint, opcua.DialTimeout(c.Timeout))
	if err != nil {
		return nil, fmt.Errorf("get endpoints: %w", err)
	}
	ep, err := selectEndpoint(eps, c.Security)
	if err != nil {
		return nil, err
	}

	token := ua.UserTokenTypeAnonymous
	if c.Username != "" {
		token = ua.UserTokenTypeUserName
		// A password is never sent on a channel that is neither signed
		// nor encrypted: the server certificate that would protect it is
		// unauthenticated there, so anyone in the path could harvest it.
		// This is also what stops a stripped endpoint list from talking
		// the "strongest offered" default down to None.
		if ep.SecurityMode == ua.MessageSecurityModeNone || ep.SecurityPolicyURI == ua.SecurityPolicyURINone {
			return nil, fmt.Errorf("refusing to send username %q on an unsecured endpoint (%s/%s): set security: and security-mode: Sign or SignAndEncrypt, or drop username: for anonymous access",
				c.Username, policyName(ep.SecurityPolicyURI), modeName(ep.SecurityMode))
		}
	}
	opts := []opcua.Option{
		opcua.SecurityFromEndpoint(ep, token),
		opcua.ApplicationURI(c.ApplicationURI),
		opcua.AutoReconnect(false),
		opcua.DialTimeout(c.Timeout),
		opcua.RequestTimeout(c.Timeout),
	}
	if c.SessionName != "" {
		opts = append(opts, opcua.SessionName(c.SessionName))
	}
	if token == ua.UserTokenTypeUserName {
		opts = append(opts, opcua.AuthUsername(c.Username, c.Password))
	}
	// A server once pinned on a secured endpoint never gets an unsecured
	// session: an endpoint list stripped down to None is how a man in the
	// middle would take a pinned server's place.
	if ep.SecurityPolicyURI == ua.SecurityPolicyURINone && c.PKIDir != "" {
		if file := PinFile(c.PKIDir, endpoint); fileExists(file) {
			return nil, fmt.Errorf("refusing an unsecured session to %s: its server was pinned on a secured endpoint (%s). "+
				"If the server was made unsecured on purpose, delete that file", endpoint, file)
		}
	}
	pinNew := false
	if ep.SecurityPolicyURI != ua.SecurityPolicyURINone {
		if c.PKIDir == "" {
			return nil, fmt.Errorf("endpoint %s needs a client certificate: set pki-dir", policyName(ep.SecurityPolicyURI))
		}
		pinned, err := checkPin(c.PKIDir, endpoint, ep.ServerCertificate)
		if err != nil {
			return nil, err
		}
		pinNew = !pinned
		certFile, keyFile, err := EnsureCertificate(c.PKIDir, c.ApplicationURI)
		if err != nil {
			return nil, err
		}
		opts = append(opts, opcua.CertificateFile(certFile), opcua.PrivateKeyFile(keyFile))
	}

	client, err := opcua.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	cctx, cancel2 := context.WithTimeout(ctx, c.Timeout)
	defer cancel2()
	if err := client.Connect(cctx); err != nil {
		return nil, fmt.Errorf("connect %s (%s/%s): %w", endpoint, policyName(ep.SecurityPolicyURI), modeName(ep.SecurityMode), err)
	}
	if pinNew {
		if err := recordPin(c.PKIDir, endpoint, ep.ServerCertificate); err != nil {
			_ = client.Close(context.Background())
			return nil, fmt.Errorf("pin the server certificate: %w", err)
		}
	}
	return &Session{Client: client, Endpoint: ep}, nil
}

// Close ends the session; errors are not interesting to a caller that is
// reconnecting anyway.
func (s *Session) Close() {
	if s != nil && s.Client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Client.Close(ctx)
	}
}

// selectEndpoint picks the endpoint matching the requested policy and mode,
// or the most secure one offered when the request is open. Ported from
// tentacle-opcua-go; stricter than gopcua's SelectEndpoint about what an
// unspecified mode means (the strongest mode of the chosen policy, never a
// silent downgrade to None).
func selectEndpoint(eps []*ua.EndpointDescription, sec Security) (*ua.EndpointDescription, error) {
	if len(eps) == 0 {
		return nil, fmt.Errorf("the server offers no endpoints")
	}
	policyURI, mode, err := sec.parse()
	if err != nil {
		return nil, err
	}

	// Strongest first: by the server's own SecurityLevel, then by mode.
	sorted := append([]*ua.EndpointDescription(nil), eps...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].SecurityLevel != sorted[j].SecurityLevel {
			return sorted[i].SecurityLevel > sorted[j].SecurityLevel
		}
		return sorted[i].SecurityMode > sorted[j].SecurityMode
	})
	for _, ep := range sorted {
		if policyURI != "" && ep.SecurityPolicyURI != policyURI {
			continue
		}
		if mode != ua.MessageSecurityModeInvalid && ep.SecurityMode != mode {
			continue
		}
		return ep, nil
	}
	var offered []string
	for _, ep := range eps {
		offered = append(offered, policyName(ep.SecurityPolicyURI)+"/"+modeName(ep.SecurityMode))
	}
	return nil, fmt.Errorf("no endpoint for policy %q mode %q; the server offers %s",
		sec.Policy, sec.Mode, strings.Join(offered, ", "))
}

// parse resolves the requested policy to its URI ("" = any) and the mode
// (MessageSecurityModeInvalid = any). Offline: New calls it so a typo in
// security: or security-mode: fails `naut check`, not the first dial.
func (sec Security) parse() (string, ua.MessageSecurityMode, error) {
	policyURI := ""
	if p := strings.ReplaceAll(strings.ToLower(sec.Policy), "_", ""); p != "" {
		for name, uri := range ua.SecurityPolicyURIs {
			if strings.ReplaceAll(strings.ToLower(name), "_", "") == p {
				policyURI = uri
			}
		}
		if policyURI == "" {
			return "", 0, fmt.Errorf("unknown security policy %q (want None, Basic256Sha256, …)", sec.Policy)
		}
	}
	var mode ua.MessageSecurityMode
	switch strings.ToLower(sec.Mode) {
	case "":
		mode = ua.MessageSecurityModeInvalid // any: prefer the strongest
	case "none":
		mode = ua.MessageSecurityModeNone
	case "sign":
		mode = ua.MessageSecurityModeSign
	case "signandencrypt":
		mode = ua.MessageSecurityModeSignAndEncrypt
	default:
		return "", 0, fmt.Errorf("unknown security mode %q (want None, Sign or SignAndEncrypt)", sec.Mode)
	}
	return policyURI, mode, nil
}

// policyName shortens a policy URI to its name ("Basic256Sha256").
func policyName(uri string) string {
	return strings.TrimPrefix(uri, ua.SecurityPolicyURIPrefix)
}

func modeName(m ua.MessageSecurityMode) string {
	switch m {
	case ua.MessageSecurityModeNone:
		return "None"
	case ua.MessageSecurityModeSign:
		return "Sign"
	case ua.MessageSecurityModeSignAndEncrypt:
		return "SignAndEncrypt"
	}
	return fmt.Sprintf("Mode(%d)", m)
}
