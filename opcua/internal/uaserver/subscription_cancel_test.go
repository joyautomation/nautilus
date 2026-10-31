package uaserver

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/opcua/internal/uaserver/internal/require"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
)

// startSubscriptionTestServer starts a server on a free port and returns its endpoint and a
// connected client; both are closed when the test ends.
func startSubscriptionTestServer(t *testing.T, ctx context.Context) (string, *opcua.Client) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	srv := New(EndPoint("127.0.0.1", port), EnableSecurity("None", ua.MessageSecurityModeNone))
	// This package leaves Read to the server that embeds it (opcua/server);
	// the client's Connect reads the NamespaceArray, so the test registers
	// the plainest Read there is.
	srv.RegisterHandler(id.ReadRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		req := r.(*ua.ReadRequest)
		res := make([]*ua.DataValue, len(req.NodesToRead))
		for i, n := range req.NodesToRead {
			ns, err := srv.Namespace(int(n.NodeID.Namespace()))
			if err != nil {
				res[i] = &ua.DataValue{EncodingMask: ua.DataValueStatusCode, Status: ua.StatusBadNodeIDUnknown}
				continue
			}
			res[i] = ns.Attribute(n.NodeID, n.AttributeID)
		}
		return &ua.ReadResponse{ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK), Results: res}, nil
	})
	// The server runs until the test's cleanup, not until ctx ends: a test
	// cancels ctx as it returns, before its cleanups run, and a server whose
	// message loop has stopped leaves the client's CloseSession and the
	// server's Close to their 10 s timeouts.
	sctx, scancel := context.WithCancel(context.Background())
	require.NoError(t, srv.Start(sctx))
	t.Cleanup(func() { _ = srv.Close(); scancel() })
	endpoint := fmt.Sprintf("opc.tcp://127.0.0.1:%d", port)

	c, err := opcua.NewClient(endpoint, opcua.SecurityMode(ua.MessageSecurityModeNone))
	require.NoError(t, err)
	require.NoError(t, c.Connect(ctx))
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return endpoint, c
}

// Subscription IDs used to be len(Subs)+1, so after a deletion a new subscription could get the
// ID of one that is still live. A cancelled subscription's task also calls DeleteSubscription
// with its own ID when it returns, and then deleted the newer subscription instead.
func TestCreateSubscriptionNeverReusesAnID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, c := startSubscriptionTestServer(t, ctx)

	subscribe := func() *opcua.Subscription {
		sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: time.Second}, make(chan *opcua.PublishNotificationData, 10))
		require.NoError(t, err)
		return sub
	}
	a, b := subscribe(), subscribe()
	require.NoError(t, a.Cancel(ctx))
	c3 := subscribe()
	require.NotEqual(t, a.SubscriptionID, c3.SubscriptionID, "a deleted subscription's ID was issued again")
	require.NotEqual(t, b.SubscriptionID, c3.SubscriptionID, "a live subscription's ID was issued again")
}
