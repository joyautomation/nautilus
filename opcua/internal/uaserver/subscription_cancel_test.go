package uaserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/gopcuatest"
	"github.com/joyautomation/nautilus/opcua/internal/require"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/uasc"
)

// startSubscriptionTestServer starts a server on a free port and returns its endpoint and a
// connected client; both are closed when the test ends.
func startSubscriptionTestServer(t *testing.T, ctx context.Context) (string, *opcua.Client) {
	t.Helper()
	endpoint, _ := startTestServer(t)
	c, err := opcua.NewClient(endpoint, opcua.SecurityMode(ua.MessageSecurityModeNone))
	require.NoError(t, err)
	require.NoError(t, gopcuatest.Connect(ctx, c))
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return endpoint, c
}

// startTestServer starts a server with no security and a plain Read handler, and returns its
// endpoint and the server. Each setup runs before Start, where handlers can be registered.
func startTestServer(t *testing.T, setup ...func(*Server)) (string, *Server) {
	t.Helper()
	var srv *Server
	var port int
	var sctx context.Context
	var scancel context.CancelFunc
	startOnFreePort(t, func(p int) error {
		port = p
		srv = New(EndPoint("127.0.0.1", port), EnableSecurity("None", ua.MessageSecurityModeNone), EnableAuthMode(ua.UserTokenTypeAnonymous))
		return startTestServerOn(t, srv, &sctx, &scancel, setup)
	})
	t.Cleanup(func() { _ = srv.Close(); scancel() })
	return fmt.Sprintf("opc.tcp://127.0.0.1:%d", port), srv
}

// startOnFreePort picks a free loopback port and calls start with it. The port is free when
// picked, not when start binds it: another process on a busy runner can take it in between
// ("bind: address already in use", #1120). That one error is retried on a new port; any
// other failure, or five in a row, fails the test.
func startOnFreePort(t *testing.T, start func(port int) error) {
	t.Helper()
	var err error
	for range 5 {
		var ln net.Listener
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := ln.Addr().(*net.TCPAddr).Port
		require.NoError(t, ln.Close())
		if err = start(port); err == nil {
			return
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			break
		}
	}
	require.NoError(t, err)
}

// startTestServerOn registers the test handlers on srv, applies setup and starts it.
func startTestServerOn(t *testing.T, srv *Server, sctx *context.Context, scancel *context.CancelFunc, setup []func(*Server)) error {
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
	var c context.Context
	c, *scancel = context.WithCancel(context.Background())
	*sctx = c
	for _, f := range setup {
		f(srv)
	}
	if err := srv.Start(c); err != nil {
		(*scancel)()
		return err
	}
	return nil
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

// A port that is taken between being picked and being bound is picked again.
func TestStartOnFreePortRetriesAnAddressInUse(t *testing.T) {
	var ports []int
	startOnFreePort(t, func(p int) error {
		ports = append(ports, p)
		if len(ports) < 3 {
			return &net.OpError{Op: "listen", Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRINUSE}}
		}
		return nil
	})
	require.Equal(t, 3, len(ports), "tries: two taken ports, then one that binds")
	for _, p := range ports {
		require.True(t, p > 0, "a real port")
	}
}
