package uaserver

import (
	"context"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

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
	return dial(t, ctx, endpoint).SecureChannel()
}

// dial is channel, returning the client so a test can drop the connection.
func dial(t *testing.T, ctx context.Context, endpoint string) *opcua.Client {
	t.Helper()
	c, err := opcua.NewClient(endpoint, opcua.SecurityPolicy(ua.SecurityPolicyURINone), opcua.SecurityMode(ua.MessageSecurityModeNone))
	require.NoError(t, err)
	require.NoError(t, c.Dial(ctx))
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
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

// The test above cannot tell crypto/rand from math/rand by the tokens alone, so it reads where
// session_broker.go gets its randomness from.
func TestSessionTokensComeFromCryptoRand(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "session_broker.go", nil, parser.ImportsOnly)
	require.NoError(t, err)
	imports := map[string]string{}
	for _, imp := range f.Imports {
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imports[strings.Trim(imp.Path.Value, `"`)] = name
	}
	name, ok := imports["crypto/rand"]
	require.True(t, ok, "session_broker.go does not import crypto/rand")
	require.True(t, name == "" || name == "rand", "crypto/rand is imported as %q, so rand.Read may be another package", name)
	_, weak := imports["math/rand"]
	_, weak2 := imports["math/rand/v2"]
	require.True(t, !weak && !weak2, "session_broker.go imports math/rand")
}

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

// A session cannot move to another channel, so when its channel goes (a client's link drops and
// it reconnects) the session and its subscriptions go with it. They were left behind for good:
// one dead session, its channel and its subscriptions per reconnect.
func TestSessionClosedWithItsChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, srv := startTestServer(t)

	sessions := func() int {
		srv.sb.mu.Lock()
		defer srv.sb.mu.Unlock()
		return len(srv.sb.s)
	}
	subs := func() int {
		srv.SubscriptionService.Mu.Lock()
		defer srv.SubscriptionService.Mu.Unlock()
		return len(srv.SubscriptionService.Subs)
	}

	keep := channel(t, ctx, endpoint) // a second client, whose session must survive
	kept := rawCreateSession(t, ctx, keep, endpoint)
	require.NoError(t, rawActivate(ctx, keep, kept))

	c := dial(t, ctx, endpoint)
	sc := c.SecureChannel()
	tok := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, rawActivate(ctx, sc, tok))
	require.NoError(t, sc.SendRequest(ctx, &ua.CreateSubscriptionRequest{RequestedPublishingInterval: 100, RequestedLifetimeCount: 300, RequestedMaxKeepAliveCount: 10, PublishingEnabled: true}, tok, func(ua.Response) error { return nil }))
	require.Equal(t, 2, sessions())
	require.Equal(t, 1, subs())

	// The client knows of no session (it was made raw), so Close sends no CloseSession: the
	// connection just goes, as a dropped link does.
	require.NoError(t, c.Close(ctx))
	deadline := time.Now().Add(5 * time.Second)
	for (sessions() != 1 || subs() != 0) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, 1, sessions(), "the dropped channel's session is still held")
	require.Equal(t, 0, subs(), "the dropped channel's subscription is still held")
	require.NoError(t, rawRead(ctx, keep, kept), "the other client's session went too")
}

// A session is closed with its channel on the message loop, after every request the channel
// sent, never while one of them is between the session gate and its handler's own lookup. The
// first version closed it from the channel's reader, so a handler mid-request could find its
// session gone (DeleteSubscriptions then dereferenced nil and took the server down).
func TestChannelCloseWaitsForItsRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	found := make(chan bool, 1)
	endpoint, srv := startTestServer(t, func(srv *Server) {
		srv.RegisterHandler(id.BrowseRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
			close(entered)
			<-release
			found <- srv.Session(r.Header()) != nil
			return &ua.BrowseResponse{ResponseHeader: responseHeader(r.Header().RequestHandle, ua.StatusOK)}, nil
		})
	})

	c := dial(t, ctx, endpoint)
	sc := c.SecureChannel()
	tok := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, rawActivate(ctx, sc, tok))
	go func() {
		req := &ua.BrowseRequest{View: &ua.ViewDescription{ViewID: ua.NewTwoByteNodeID(0)}}
		_ = sc.SendRequest(ctx, req, tok, func(ua.Response) error { return nil })
	}()
	<-entered
	// The handler has passed the gate and holds the loop. Drop the connection under it and
	// give the channel's reader time to see the end.
	require.NoError(t, c.Close(ctx))
	time.Sleep(300 * time.Millisecond)
	close(release)
	require.True(t, <-found, "the session was closed while its request was being handled")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srv.sb.mu.Lock()
		n := len(srv.sb.s)
		srv.sb.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the session outlived its channel")
}
