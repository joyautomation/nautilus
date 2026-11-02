package uasc

import (
	"sync/atomic"
	"testing"
	"time"

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

// A renewal OPN is held to the channel's own policy and certificate before
// anything is decrypted with it, and gets a new instance (a new token on the
// same channel and sequence counter) rather than the live one's keys.
func TestRenewalInstanceHoldsTheChannelSecurity(t *testing.T) {
	active := &channelInstance{secureChannelID: 7, securityTokenID: 41, seq: new(atomic.Uint32)}
	s := &SecureChannel{kind: server, cfg: &Config{}, activeInstance: active}
	s.secOpened, s.sec = true, channelSecurity{policy: ua.SecurityPolicyURIBasic256Sha256, mode: ua.MessageSecurityModeSignAndEncrypt, cert: []byte("A")}
	if _, err := s.renewalInstance(ua.SecurityPolicyURINone, nil); err != ua.StatusBadSecurityChecksFailed {
		t.Errorf("renewal down to None: %v, want Bad_SecurityChecksFailed", err)
	}
	if _, err := s.renewalInstance(ua.SecurityPolicyURIBasic256Sha256, []byte("B")); err != ua.StatusBadSecurityChecksFailed {
		t.Errorf("renewal with another certificate: %v, want Bad_SecurityChecksFailed", err)
	}

	s.sec = channelSecurity{policy: ua.SecurityPolicyURINone, mode: ua.MessageSecurityModeNone}
	inst, err := s.renewalInstance(ua.SecurityPolicyURINone, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst == active || inst.secureChannelID != 7 || inst.securityTokenID != 42 || inst.seq != active.seq {
		t.Errorf("renewal instance %+v: want a new instance, channel 7, token 42, the channel's counter", inst)
	}
}

// The granted token lifetime is at most an hour.
func TestRevisedLifetimeIsCapped(t *testing.T) {
	for req, want := range map[uint32]uint32{0: maxLifetime, 1500: 1500, maxLifetime: maxLifetime, 50 * maxLifetime: maxLifetime} {
		if got := revisedLifetime(req); got != want {
			t.Errorf("revisedLifetime(%d) = %d, want %d", req, got, want)
		}
	}
}

// A server channel whose token runs out without a renewal is closed; one
// that renewed is not.
func TestTokenExpiryClosesAnUnrenewedChannel(t *testing.T) {
	closed := make(chan struct{}, 1)
	s := &SecureChannel{kind: server, closeConn: func() { closed <- struct{}{} }}
	inst := &channelInstance{revisedLifetime: 40 * time.Millisecond}
	s.activeInstance = inst
	s.expireUnlessRenewed(inst)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("an expired, unrenewed channel was not closed")
	}

	renewed := &channelInstance{revisedLifetime: 40 * time.Millisecond}
	s.activeInstance = renewed
	s.expireUnlessRenewed(inst) // the old token's timer
	select {
	case <-closed:
		t.Fatal("a renewed channel was closed when its old token expired")
	case <-time.After(150 * time.Millisecond):
	}
}
