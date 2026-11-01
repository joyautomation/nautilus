package uaserver

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
	"github.com/joyautomation/nautilus/opcua/internal/uaserver/internal/require"
)

// Close returns promptly with a client's session still open, whether the
// server's context is still live or was cancelled first (opcua/server's
// Stop cancels, then closes). gopcua's channel broker closed only the
// secure channels, not the connections under them, so each channel's
// reader stayed in a blocking read and Close waited out a 10 s timeout
// whenever a session was open (nautilus#423, black-box F-14).
func TestCloseWithSessionOpenIsPrompt(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelFirst=%v", cancelFirst), func(t *testing.T) {
			srv, scancel, endpoint := startCloseTestServer(t)
			c, err := opcua.NewClient(endpoint, opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AutoReconnect(false))
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, c.Connect(ctx))
			t.Cleanup(func() { _ = c.Close(context.Background()) })
			// One round trip on the session, so the channel's reader is
			// parked in its next read when Close comes.
			_, err = c.Node(ua.NewNumericNodeID(0, id.Server_NamespaceArray)).Value(ctx)
			require.NoError(t, err)

			if cancelFirst {
				scancel()
			}
			start := time.Now()
			require.NoError(t, srv.Close())
			if d := time.Since(start); d > time.Second {
				t.Fatalf("Close with a session open took %v; want well under a second", d)
			}
			// The listener is gone: the port takes a new bind at once.
			ln, err := net.Listen("tcp", endpoint[len("opc.tcp://"):])
			require.NoError(t, err)
			ln.Close()
		})
	}
}

// startCloseTestServer starts a server on a free port with the plainest
// Read (this package leaves Read to opcua/server; a client's Connect reads
// the NamespaceArray). The caller closes it; cleanup cancels its context.
func startCloseTestServer(t *testing.T) (*Server, context.CancelFunc, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	srv := New(EndPoint("127.0.0.1", port), EnableSecurity("None", ua.MessageSecurityModeNone), EnableAuthMode(ua.UserTokenTypeAnonymous))
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
	sctx, scancel := context.WithCancel(context.Background())
	require.NoError(t, srv.Start(sctx))
	t.Cleanup(scancel)
	return srv, scancel, fmt.Sprintf("opc.tcp://127.0.0.1:%d", port)
}
