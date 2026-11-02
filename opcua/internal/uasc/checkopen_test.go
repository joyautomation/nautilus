package uasc

import (
	"testing"

	"github.com/gopcua/opcua/ua"
)

// checkOpen is the server side's gate on every OpenSecureChannel: the
// renewal rule, the policy/mode pairing, the allowlist and the trust hook,
// in that order.
func TestCheckOpen(t *testing.T) {
	none := channelSecurity{policy: ua.SecurityPolicyURINone, mode: ua.MessageSecurityModeNone}
	secure := channelSecurity{policy: ua.SecurityPolicyURIBasic256Sha256, mode: ua.MessageSecurityModeSignAndEncrypt, cert: []byte("client A")}
	allow := func(p string, m ua.MessageSecurityMode) bool {
		return (p == none.policy && m == none.mode) || (p == secure.policy && m == secure.mode)
	}
	var verified [][]byte
	verify := func(cert []byte) error {
		verified = append(verified, cert)
		if string(cert) == "client B" {
			return ua.StatusBadCertificateUntrusted
		}
		return nil
	}

	cases := []struct {
		name   string
		opened *channelSecurity // the channel's security before this OPN; nil = first
		want   channelSecurity
		code   error
	}{
		{"none", nil, none, nil},
		{"secure, trusted", nil, secure, nil},
		{"secure, untrusted", nil, channelSecurity{policy: secure.policy, mode: secure.mode, cert: []byte("client B")}, ua.StatusBadCertificateUntrusted},
		{"not offered: Sign", nil, channelSecurity{policy: secure.policy, mode: ua.MessageSecurityModeSign, cert: secure.cert}, ua.StatusBadSecurityPolicyRejected},
		{"not offered: Basic256", nil, channelSecurity{policy: ua.SecurityPolicyURIBasic256, mode: secure.mode, cert: secure.cert}, ua.StatusBadSecurityPolicyRejected},
		{"policy None, mode secure", nil, channelSecurity{policy: none.policy, mode: ua.MessageSecurityModeSignAndEncrypt}, ua.StatusBadSecurityPolicyRejected},
		{"policy secure, mode None", nil, channelSecurity{policy: secure.policy, mode: ua.MessageSecurityModeNone, cert: secure.cert}, ua.StatusBadSecurityPolicyRejected},
		{"mode invalid", nil, channelSecurity{policy: secure.policy, mode: ua.MessageSecurityModeInvalid, cert: secure.cert}, ua.StatusBadSecurityPolicyRejected},
		{"renewal, same", &secure, secure, nil},
		{"renewal down to None", &secure, none, ua.StatusBadSecurityChecksFailed},
		{"renewal with another certificate", &secure, channelSecurity{policy: secure.policy, mode: secure.mode, cert: []byte("client C")}, ua.StatusBadSecurityChecksFailed},
		{"renewal up from None", &none, secure, ua.StatusBadSecurityChecksFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &SecureChannel{cfg: &Config{Allow: allow, VerifyPeer: verify}}
			if c.opened != nil {
				s.secOpened, s.sec = true, *c.opened
			}
			if err := s.checkOpen(c.want); err != c.code {
				t.Errorf("checkOpen = %v, want %v", err, c.code)
			}
		})
	}

	// The trust hook is not asked about a None channel, and is asked again
	// at each renewal of a secured one.
	verified = nil
	s := &SecureChannel{cfg: &Config{Allow: allow, VerifyPeer: verify}}
	if err := s.checkOpen(none); err != nil || len(verified) != 0 {
		t.Errorf("None: %v, verified %d certificates", err, len(verified))
	}
	s = &SecureChannel{cfg: &Config{Allow: allow, VerifyPeer: verify}}
	s.secOpened, s.sec = true, secure
	if err := s.checkOpen(secure); err != nil || len(verified) != 1 {
		t.Errorf("renewal: %v, verified %d certificates, want 1", err, len(verified))
	}
}
