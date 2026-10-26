package uaserver

import (
	"context"
	"strings"
	"testing"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/uaserver/internal/require"
)

// createSession sends a CreateSession naming endpointURL on c's secure
// channel and returns the server's answer.
func createSession(t *testing.T, ctx context.Context, c *opcua.Client, endpointURL string) *ua.CreateSessionResponse {
	t.Helper()
	req := &ua.CreateSessionRequest{
		ClientDescription:       &ua.ApplicationDescription{ApplicationURI: "urn:test", ApplicationName: ua.NewLocalizedText("test"), ApplicationType: ua.ApplicationTypeClient},
		EndpointURL:             endpointURL,
		SessionName:             "test",
		ClientNonce:             make([]byte, 32),
		RequestedSessionTimeout: 60000,
	}
	var resp *ua.CreateSessionResponse
	require.NoError(t, c.Send(ctx, req, func(r ua.Response) error {
		resp = r.(*ua.CreateSessionResponse)
		return nil
	}))
	return resp
}

// Part 4 §5.6.2: CreateSession returns the endpoints available for the
// server; endpointUrl only helps choose which URLs to return. A client that
// dialled an address the server was not configured with (a NAT address, a
// DNS alias) got an empty ServerEndpoints and refused the session.
func TestCreateSessionServerEndpointsForAnUnknownURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, c := startSubscriptionTestServer(t, ctx)

	resp := createSession(t, ctx, c, "opc.tcp://alias.example:4840")
	require.NotEmpty(t, resp.ServerEndpoints, "no endpoints for an address the server does not know")
	require.Equal(t, endpoint, resp.ServerEndpoints[0].EndpointURL)

	// A known URL, in another case and with a trailing slash, still picks
	// its own endpoint.
	resp = createSession(t, ctx, c, strings.ToUpper(endpoint)+"/")
	require.Len(t, resp.ServerEndpoints, 1)
	require.Equal(t, endpoint, resp.ServerEndpoints[0].EndpointURL)
}
