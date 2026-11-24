package node

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum"
)

// Every member pins the other two's keys at their first contact (#710
// per-box keys): each controller its peer and the witness, the witness
// both controllers under the pair's cluster; each pin is the member's
// own key.
func TestLoopbackFirstContactPinsAllThree(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	own := func(id quorum.NodeID) string {
		fp, err := c.peerTLS(id).Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	c.waitFor("every pin", leases(20), func() bool {
		return len(c.peerTLS("a").Pinned("")) == 2 && len(c.peerTLS("b").Pinned("")) == 2 && len(c.peerTLS(WitnessID).Pinned(c.pair)) == 2
	})
	a, b, w := c.peerTLS("a").Pinned(""), c.peerTLS("b").Pinned(""), c.peerTLS(WitnessID).Pinned(c.pair)
	if a["b"] != own("b") || a[peertls.Witness] != own(WitnessID) || b["a"] != own("a") || b[peertls.Witness] != own(WitnessID) || w["a"] != own("a") || w["b"] != own("b") {
		t.Fatalf("pins: a %v, b %v, witness %v", a, b, w)
	}
}

// bound is a request as a peer listener hands it on with peer TLS: a TLS
// connection, and the member its certificate names (none when name is
// "").
func bound(r *http.Request, cluster, name string) *http.Request {
	r.TLS = &tls.ConnectionState{}
	if name == "" {
		return r
	}
	return WithPeer(r, cluster, name)
}

// Every handler that takes a sender from the request checks it against
// the member the connection's certificate names (#710 per-box keys): a
// pinned member may not speak as another, nor in another cluster. A
// controller's listener takes calls from its peer only, so there the
// check is behind the handshake's; these requests reach it directly.
func TestEveryHandlerBindsItsSender(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	other := NewClusterID()
	b := c.elector("b").Handler()
	w := c.wit.w.Handler()
	base := Prefix + c.pair
	vote, _ := quorum.Request{From: "a", Nonce: 3, ID: 1}.MarshalBinary()
	adopt, _ := quorum.Adopt{From: "a", Kind: quorum.State, Nonce: 7}.MarshalBinary()
	send := func(h http.Handler, method, path string, body []byte, members string, r func(*http.Request) *http.Request) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set(LeaseHeader, strconv.FormatInt(int64(c.lease), 10))
		if members != "" {
			req.Header.Set(MembersHeader, members)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r(req))
		return rec
	}
	asName := func(cl, n string) func(*http.Request) *http.Request {
		return func(r *http.Request) *http.Request { return bound(r, cl, n) }
	}
	for _, tc := range []struct {
		name    string
		h       http.Handler
		method  string
		path    string
		body    []byte
		members string
		mut     string
		// anyMember: a request that names no sender, taken from either
		// controller of the cluster (the witness's marks).
		anyMember bool
	}{
		{"b's voter: a vote", b, "POST", base + "/vote", vote, "a,b", "sender_unbound_vote", false},
		{"b's voter: an adopt", b, "POST", base + "/adopt", adopt, "a,b", "sender_unbound_adopt", false},
		{"b's hello", b, "GET", base + "/hello?c=0123456789abcdef&from=a", nil, "a,b", "sender_unbound_hello", false},
		{"b's marks", b, "POST", base + "/dup", []byte(`{"op":"state"}`), "a,b", "sender_unbound_dup", false},
		{"the witness's voter: a vote", w, "POST", base + "/vote", vote, "a,b", "witness_sender_unbound_vote", false},
		{"the witness's marks", w, "POST", base + "/dup", []byte(`{"op":"state"}`), "a,b", "witness_sender_unbound_dup", true},
	} {
		// The member the request names, of this cluster: past the binding.
		if rec := send(tc.h, tc.method, tc.path, tc.body, tc.members, asName(c.pair, "a")); rec.Code == http.StatusForbidden {
			t.Errorf("%s from a, as a: refused: %s", tc.name, rec.Body)
		}
		for _, as := range []struct{ desc, cl, n string }{
			{"as b", c.pair, "b"},
			{"as the witness", "", peertls.Witness},
			{"as the witness of this cluster", c.pair, peertls.Witness},
			{"as another cluster's a", other, "a"},
			{"with no verified member", "", ""},
		} {
			rec := send(tc.h, tc.method, tc.path, tc.body, tc.members, asName(as.cl, as.n))
			if tc.anyMember && as.n == "b" && as.cl == c.pair {
				if rec.Code == http.StatusForbidden {
					t.Errorf("%s from b: refused: %s", tc.name, rec.Body)
				}
				continue
			}
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s naming a, %s (row %s): %d %s, want 403", tc.name, as.desc, tc.mut, rec.Code, strings.TrimSpace(rec.Body.String()))
			}
		}
	}
}
