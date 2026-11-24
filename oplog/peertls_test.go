package oplog

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With peer TLS, the log's calls go out as https under the URLs it was
// given, on a transport whose dial and handshake are bounded: a peer that
// accepts and never speaks holds no connection past them (#710, #1230
// review). The handshake is peer TLS's own (Options.TLS), handed the
// address dialed, so it checks the member at that address.
func TestPeerTLSWireIsBounded(t *testing.T) {
	var wrapped string
	n, err := New(Options{Self: "http://a:1", Peers: []string{"http://a:1", "http://b:1"}, Token: "t",
		Leadership: Static{Self: "http://a:1", Leader: "http://a:1"}, Ephemeral: true,
		TLS: func(ctx context.Context, raw net.Conn, addr string) (net.Conn, error) {
			wrapped = addr
			if _, ok := ctx.Deadline(); !ok {
				t.Error("the handshake has no deadline")
			}
			raw.Close()
			return nil, errors.New("test: no handshake")
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	w, ok := n.client.(*http.Client).Transport.(tlsWire)
	if !ok {
		t.Fatalf("the log's transport is %T, want tlsWire", n.client.(*http.Client).Transport)
	}
	tr := w.rt.(*http.Transport)
	if tr.DialTLSContext == nil || tr.DialContext == nil || tr.Proxy != nil {
		t.Fatalf("transport: TLS dial %v, dialer %v, proxy %v", tr.DialTLSContext != nil, tr.DialContext != nil, tr.Proxy != nil)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()
	req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/api/quorum", nil)
	_, _ = n.client.(*http.Client).Do(req)
	if wrapped != ln.Addr().String() {
		t.Fatalf("the handshake was handed %q, want the dialed %s", wrapped, ln.Addr())
	}
	req, _ = http.NewRequest(http.MethodGet, "http://b:1/api/quorum", nil)
	var seen string
	w2 := tlsWire{rt: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.URL.String()
		return nil, http.ErrHandlerTimeout
	})}
	_, _ = w2.RoundTrip(req)
	if seen != "https://b:1/api/quorum" || req.URL.Scheme != "http" {
		t.Fatalf("sent %q (caller's URL now %q), want https on the wire and the caller's URL untouched", seen, req.URL)
	}
}

// Replication is bound to its connection (#710 per-box keys): a request
// naming a member that is not the one its peer TLS certificate names is
// refused before the leader or Admit is consulted.
func TestReplicationIsBoundToItsSender(t *testing.T) {
	var asked []string
	n, err := New(Options{Self: "http://a:1", Peers: []string{"http://a:1", "http://b:1"}, Token: "t",
		Leadership: Static{Self: "http://a:1", Leader: "http://b:1"}, Ephemeral: true,
		Sender: func(r *http.Request, node, from string) bool {
			asked = append(asked, node+"@"+from)
			return node == "b" && from == "http://b:1"
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	h := n.Handler()
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/oplog/replicate", strings.NewReader(body))
		r.Header.Set("X-Nautilus-Cluster", "t")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"from":"http://b:1","node":"a","term":1}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "certificate names another member") {
		t.Fatalf("replication naming a from b's connection: %d %s", w.Code, w.Body)
	}
	if w := post(`{"from":"http://b:1","node":"b","term":1}`); w.Code == http.StatusForbidden {
		t.Fatalf("b's own replication refused: %s", w.Body)
	}
	if len(asked) != 2 || asked[0] != "a@http://b:1" {
		t.Fatalf("Sender asked %v", asked)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
