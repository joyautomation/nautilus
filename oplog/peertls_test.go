package oplog

import (
	"crypto/tls"
	"net/http"
	"testing"
)

// With peer TLS, the log's calls go out as https under the URLs it was
// given, on a transport whose dial and handshake are bounded: a peer that
// accepts and never speaks holds no connection past them (#710, #1230
// review).
func TestPeerTLSWireIsBounded(t *testing.T) {
	n, err := New(Options{Self: "http://a:1", Peers: []string{"http://a:1", "http://b:1"}, Token: "t",
		Leadership: Static{Self: "http://a:1", Leader: "http://a:1"}, Ephemeral: true, TLS: &tls.Config{MinVersion: tls.VersionTLS13}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	w, ok := n.client.Transport.(tlsWire)
	if !ok {
		t.Fatalf("the log's transport is %T, want tlsWire", n.client.Transport)
	}
	tr := w.rt.(*http.Transport)
	if tr.TLSClientConfig == nil || tr.TLSHandshakeTimeout <= 0 || tr.DialContext == nil || tr.Proxy != nil {
		t.Fatalf("transport: TLS %v, handshake bound %v, dialer %v, proxy %v", tr.TLSClientConfig != nil, tr.TLSHandshakeTimeout, tr.DialContext != nil, tr.Proxy != nil)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://b:1/api/quorum", nil)
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
