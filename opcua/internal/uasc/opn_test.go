package uasc

import (
	"io"
	"testing"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uacp"
	"github.com/joyautomation/nautilus/opcua/internal/require"
)

// newServerChannel is a server channel over a real TCP pair, with whatever
// the client side receives drained, and closes recorded on closed.
func newServerChannel(t *testing.T) (*SecureChannel, chan struct{}) {
	t.Helper()
	serverTCP, clientTCP := newTestTCPConnPair(t)
	t.Cleanup(func() { serverTCP.Close(); clientTCP.Close() })
	go io.Copy(io.Discard, clientTCP)
	conn, err := uacp.NewConn(serverTCP, &uacp.Acknowledge{ReceiveBufSize: 64 * 1024, SendBufSize: 64 * 1024, MaxMessageSize: 2 * 1024 * 1024, MaxChunkCount: 512})
	require.NoError(t, err)
	s, err := NewServerSecureChannel("opc.tcp://127.0.0.1", conn, &Config{SecurityPolicyURI: ua.SecurityPolicyURINone, SecurityMode: ua.MessageSecurityModeNone}, make(chan error, 4), 7, 100, 41)
	require.NoError(t, err)
	closed := make(chan struct{}, 4)
	s.closeConn = func() { closed <- struct{}{} }
	t.Cleanup(s.Release)
	return s, closed
}

func opnRequest(mode ua.MessageSecurityMode, lifetimeMs uint32, kind ua.SecurityTokenRequestType) *ua.OpenSecureChannelRequest {
	return &ua.OpenSecureChannelRequest{
		RequestHeader:     &ua.RequestHeader{AuthenticationToken: ua.NewTwoByteNodeID(0)},
		RequestType:       kind,
		SecurityMode:      mode,
		ClientNonce:       []byte{},
		RequestedLifetime: lifetimeMs,
	}
}

// The OPN handler grants at most an hour, whatever the client asks.
func TestOpenGrantsAtMostAnHour(t *testing.T) {
	s, _ := newServerChannel(t)
	require.NoError(t, s.handleOpenSecureChannelRequest(1, opnRequest(ua.MessageSecurityModeNone, 0xFFFFFFFF, ua.SecurityTokenRequestTypeIssue)))
	inst, err := s.getActiveChannelInstance()
	require.NoError(t, err)
	require.Equal(t, time.Hour, inst.revisedLifetime)
}

// A channel opened with a short token that its client never renews is
// closed by the server: the handler arms the expiry itself.
func TestOpenArmsTheTokenExpiry(t *testing.T) {
	s, closed := newServerChannel(t)
	require.NoError(t, s.handleOpenSecureChannelRequest(1, opnRequest(ua.MessageSecurityModeNone, 40, ua.SecurityTokenRequestTypeIssue)))
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("an unrenewed channel was not closed")
	}
}

// A renewal goes through checkOpen too: one that asks for other security
// than the channel opened with is refused, and the channel's token is kept.
func TestRenewalIsCheckedLikeAnOpen(t *testing.T) {
	s, _ := newServerChannel(t)
	require.NoError(t, s.handleOpenSecureChannelRequest(1, opnRequest(ua.MessageSecurityModeNone, 60000, ua.SecurityTokenRequestTypeIssue)))
	before, _ := s.getActiveChannelInstance()

	inst, err := s.renewalInstance(ua.SecurityPolicyURINone, nil)
	require.NoError(t, err)
	s.openingInstance = inst
	err = s.handleOpenSecureChannelRequest(2, opnRequest(ua.MessageSecurityModeSign, 60000, ua.SecurityTokenRequestTypeRenew))
	if err == nil {
		t.Fatal("a renewal asking for Sign on a None channel was accepted")
	}
	after, _ := s.getActiveChannelInstance()
	if after != before {
		t.Error("a refused renewal replaced the channel's token")
	}

	inst, err = s.renewalInstance(ua.SecurityPolicyURINone, nil)
	require.NoError(t, err)
	s.openingInstance = inst
	require.NoError(t, s.handleOpenSecureChannelRequest(3, opnRequest(ua.MessageSecurityModeNone, 60000, ua.SecurityTokenRequestTypeRenew)))
	after, _ = s.getActiveChannelInstance()
	if after == before || after.securityTokenID != before.securityTokenID+1 {
		t.Errorf("a good renewal did not install a new token (%d after %d)", after.securityTokenID, before.securityTokenID)
	}
}
