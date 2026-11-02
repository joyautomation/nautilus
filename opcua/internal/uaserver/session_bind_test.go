package uaserver

import (
	"context"
	"testing"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
	"github.com/joyautomation/nautilus/opcua/internal/uaserver/internal/require"
)

// channel opens a secure channel to endpoint with no session on it, so a test can send any
// request with any authentication token.
func channel(t *testing.T, ctx context.Context, endpoint string) *uasc.SecureChannel {
	t.Helper()
	c, err := opcua.NewClient(endpoint, opcua.SecurityPolicy(ua.SecurityPolicyURINone), opcua.SecurityMode(ua.MessageSecurityModeNone))
	require.NoError(t, err)
	require.NoError(t, c.Dial(ctx))
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c.SecureChannel()
}

// rawCreateSession creates a session on sc and returns its authentication token.
func rawCreateSession(t *testing.T, ctx context.Context, sc *uasc.SecureChannel, endpoint string) *ua.NodeID {
	t.Helper()
	req := &ua.CreateSessionRequest{
		ClientDescription:       &ua.ApplicationDescription{ApplicationURI: "urn:test", ApplicationName: ua.NewLocalizedText("test"), ApplicationType: ua.ApplicationTypeClient},
		EndpointURL:             endpoint,
		SessionName:             "test",
		ClientNonce:             make([]byte, 32),
		RequestedSessionTimeout: 60000,
	}
	var tok *ua.NodeID
	require.NoError(t, sc.SendRequest(ctx, req, nil, func(r ua.Response) error {
		tok = r.(*ua.CreateSessionResponse).AuthenticationToken
		return nil
	}))
	return tok
}

// rawActivate activates the session tok names, anonymously, on sc.
func rawActivate(ctx context.Context, sc *uasc.SecureChannel, tok *ua.NodeID) error {
	req := &ua.ActivateSessionRequest{
		ClientSignature:    &ua.SignatureData{},
		UserIdentityToken:  ua.NewExtensionObject(&ua.AnonymousIdentityToken{PolicyID: "Anonymous"}),
		UserTokenSignature: &ua.SignatureData{},
	}
	return sc.SendRequest(ctx, req, tok, func(ua.Response) error { return nil })
}

// rawRead reads the server's NamespaceArray on sc with tok as the authentication token.
func rawRead(ctx context.Context, sc *uasc.SecureChannel, tok *ua.NodeID) error {
	req := &ua.ReadRequest{
		NodesToRead: []*ua.ReadValueID{{NodeID: ua.NewNumericNodeID(0, id.Server_NamespaceArray), AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}}},
	}
	return sc.SendRequest(ctx, req, tok, func(ua.Response) error { return nil })
}

// A session's authentication token is its only credential once activated, so it must not be
// guessable. It was a math/rand Int31 in a numeric NodeId: 31 bits from an unseeded generator.
func TestSessionTokensAreRandomAndLong(t *testing.T) {
	sb := newSessionBroker(nil)
	seen := map[string]bool{}
	for range 64 {
		s, err := sb.NewSession(nil)
		require.NoError(t, err)
		tok := s.AuthTokenID
		require.Equal(t, ua.NodeIDTypeByteString, tok.Type(), "token %v is not a ByteString NodeId", tok)
		require.Equal(t, uint16(0), tok.Namespace())
		require.Len(t, tok.StringID(), base64Len(authTokenLength))
		require.True(t, !seen[tok.String()], "token %v repeated", tok)
		seen[tok.String()] = true
		require.True(t, sb.Session(tok) == s, "the broker does not find a session by its token")
	}
}

func base64Len(n int) int { return (n + 2) / 3 * 4 }

// A token presented on a channel other than the one its session was created and activated on
// is refused, so a token read off the wire cannot be used from a second connection.
func TestSessionTokenRefusedOnAnotherChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, _ := startSubscriptionTestServer(t, ctx)

	a := channel(t, ctx, endpoint)
	tok := rawCreateSession(t, ctx, a, endpoint)
	require.NoError(t, rawActivate(ctx, a, tok))
	require.NoError(t, rawRead(ctx, a, tok), "the owning channel is refused")

	b := channel(t, ctx, endpoint)
	require.Equal(t, ua.StatusBadSecureChannelIDInvalid, rawRead(ctx, b, tok))
	// Not by activating it there either: that would move the session to b.
	require.Equal(t, ua.StatusBadSecureChannelIDInvalid, rawActivate(ctx, b, tok))
	require.NoError(t, rawRead(ctx, a, tok), "the session still belongs to a")

	// A session created on b but never activated cannot be activated from a.
	tok2 := rawCreateSession(t, ctx, b, endpoint)
	require.Equal(t, ua.StatusBadSecureChannelIDInvalid, rawActivate(ctx, a, tok2))
	require.NoError(t, rawActivate(ctx, b, tok2))
}

// Every service but discovery, CreateSession and ActivateSession needs an activated session;
// handleService used to dispatch whatever arrived.
func TestServiceCallsNeedAnActivatedSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, _ := startSubscriptionTestServer(t, ctx)
	sc := channel(t, ctx, endpoint)

	// No session at all: no token, or one the server never issued.
	require.Equal(t, ua.StatusBadSessionIDInvalid, rawRead(ctx, sc, nil))
	require.Equal(t, ua.StatusBadSessionIDInvalid, rawRead(ctx, sc, ua.NewByteStringNodeID(0, make([]byte, authTokenLength))))
	err := sc.SendRequest(ctx, &ua.CreateSubscriptionRequest{RequestedPublishingInterval: 100, RequestedLifetimeCount: 30, RequestedMaxKeepAliveCount: 10, PublishingEnabled: true}, nil, func(ua.Response) error { return nil })
	require.Equal(t, ua.StatusBadSessionIDInvalid, err)

	// Discovery needs no session. This bare server has no GetEndpoints handler (opcua/server
	// registers one), so getting past the gate means Bad_ServiceUnsupported.
	err = sc.SendRequest(ctx, &ua.GetEndpointsRequest{EndpointURL: endpoint}, nil, func(ua.Response) error { return nil })
	require.Equal(t, ua.StatusBadServiceUnsupported, err)

	// Created, not activated: refused until ActivateSession.
	tok := rawCreateSession(t, ctx, sc, endpoint)
	require.Equal(t, ua.StatusBadSessionNotActivated, rawRead(ctx, sc, tok))
	require.NoError(t, rawActivate(ctx, sc, tok))
	require.NoError(t, rawRead(ctx, sc, tok))

	// A session can be closed without being activated.
	tok2 := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, sc.SendRequest(ctx, &ua.CloseSessionRequest{DeleteSubscriptions: true}, tok2, func(ua.Response) error { return nil }))
	require.Equal(t, ua.StatusBadSessionIDInvalid, rawRead(ctx, sc, tok2))
}
