package webpush

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testSub(n int) Subscription {
	return Subscription{
		Endpoint: fmt.Sprintf("https://push.example.net/p/%d", n),
		P256dh:   b64.EncodeToString(append([]byte{4}, make([]byte, 64)...)),
		Auth:     b64.EncodeToString(make([]byte, 16)),
	}
}

func newReg(t *testing.T) (*Registry, *Key) {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewRegistry(k)
	if err != nil {
		t.Fatal(err)
	}
	return g, k
}

var t0 = time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)

func TestRegistrySealsAndOpens(t *testing.T) {
	g, _ := newReg(t)
	s := testSub(1)
	row, _, err := g.Add("user:ann", nil, "c1", "push.example.net", s, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{s.Endpoint, s.P256dh, s.Auth, "p/1"} {
		if strings.Contains(row.Sealed, secret) || strings.Contains(fmt.Sprint(row), secret) {
			t.Errorf("a row shows %q", secret)
		}
	}
	got, err := g.Open(row)
	if err != nil || got != s {
		t.Fatalf("Open = %v, %v", got, err)
	}
	// Moved to another principal, or altered: sealed, not opened.
	moved := row
	moved.Principal = "user:bob"
	if _, err := g.Open(moved); err != ErrSealed {
		t.Errorf("a row moved to another principal opened: %v", err)
	}
	// Another key cannot open it, and does not drop it.
	g2, _ := newReg(t)
	g2.Load(g.Rows())
	if _, err := g2.Open(row); err != ErrSealed {
		t.Errorf("another key opened the row: %v", err)
	}
	if g2.Sealed() != 1 || len(g2.Rows()) != 1 {
		t.Errorf("a row under another key is kept and counted: sealed %d rows %d", g2.Sealed(), len(g2.Rows()))
	}
}

func TestRegistryEndpointTakeoverAndCap(t *testing.T) {
	g, _ := newReg(t)
	g.Add("user:ann", nil, "c", "h", testSub(1), t0)
	_, ch, _ := g.Add("user:bob", nil, "c", "h", testSub(1), t0.Add(time.Second))
	if len(ch.Taken) != 1 || ch.Taken[0].Principal != "user:ann" {
		t.Fatalf("the newest principal takes the endpoint: %+v", ch)
	}
	if len(g.Of("user:ann")) != 0 || len(g.Of("user:bob")) != 1 {
		t.Error("ann still holds the endpoint")
	}
	_, ch, _ = g.Add("user:bob", nil, "c", "h", testSub(1), t0.Add(2*time.Second))
	if !ch.Replace || len(g.Of("user:bob")) != 1 {
		t.Errorf("re-subscribing replaces: %+v", ch)
	}
	for i := 2; i <= MaxPerPrincipal+2; i++ {
		g.Add("user:ann", nil, "c", "h", testSub(i), t0.Add(time.Duration(i)*time.Minute))
	}
	rows := g.Of("user:ann")
	if len(rows) != MaxPerPrincipal {
		t.Fatalf("ann has %d, cap %d", len(rows), MaxPerPrincipal)
	}
	for _, r := range rows {
		if r.ID == rowID(testSub(2).Endpoint) {
			t.Error("the oldest was kept, a newer one ended")
		}
	}
	if len(g.Of("user:bob")) != 1 {
		t.Error("the cap is per principal")
	}
}

func TestRegistryRemove(t *testing.T) {
	g, _ := newReg(t)
	r, _, _ := g.Add("user:ann", nil, "c", "h", testSub(1), t0)
	if _, ok := g.Remove("user:bob", r.ID); ok {
		t.Error("bob removed ann's subscription")
	}
	_, first := g.Remove("user:ann", r.ID)
	_, second := g.Remove("user:ann", r.ID)
	if !first || second {
		t.Error("remove is once, by its owner")
	}
}

func TestNormalizeKeys(t *testing.T) {
	if _, err := Normalize(testSub(1)); err == nil {
		t.Fatal("an all-zero point is not on the curve")
	}
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	au := make([]byte, 16)
	rand.Read(au)
	good := Subscription{Endpoint: "https://x", P256dh: b64.EncodeToString(ua.PublicKey().Bytes()), Auth: b64.EncodeToString(au)}
	// A browser's padded form is made the one form Send decodes.
	padded := good
	padded.Auth = base64.URLEncoding.EncodeToString(au)
	padded.P256dh = base64.URLEncoding.EncodeToString(ua.PublicKey().Bytes())
	got, err := Normalize(padded)
	if err != nil || got != good {
		t.Fatalf("padded keys: %v %+v", err, got)
	}
	for name, mut := range map[string]func(*Subscription){
		"short p256dh":  func(s *Subscription) { s.P256dh = b64.EncodeToString(make([]byte, 64)) },
		"off the curve": func(s *Subscription) { s.P256dh = b64.EncodeToString(append([]byte{4}, make([]byte, 64)...)) },
		"compressed":    func(s *Subscription) { s.P256dh = b64.EncodeToString(append([]byte{2}, make([]byte, 64)...)) },
		"short auth":    func(s *Subscription) { s.Auth = b64.EncodeToString(make([]byte, 15)) },
		"not base64":    func(s *Subscription) { s.Auth = "!!!" },
	} {
		s := good
		mut(&s)
		if _, err := Normalize(s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPolicyCheckEndpoint(t *testing.T) {
	open := Policy{}
	for _, ok := range []string{"https://fcm.googleapis.com/fcm/send/abc", "https://updates.push.services.mozilla.com/wpush/v2/x", "https://8.8.8.8/p"} {
		if _, err := open.CheckEndpoint(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"https://fcm.googleapis.com:8443/x", "https://[64:ff9b::a00:1]/p", "https://[2002:a00:1::1]/p", "https://[::ffff:10.0.0.1]/p",
		"https://0.0.0.0/p", "https://198.18.0.1/p", "https://240.0.0.1/p", "https://[fec0::1]/p",
		"http://fcm.googleapis.com/x", "https://user:pw@fcm.googleapis.com/x", "https:///x", "https://x/#f", "ftp://x/y", "",
		"https://127.0.0.1/p", "https://10.0.0.5/p", "https://192.168.1.2/p", "https://169.254.169.254/latest", "https://[::1]/p",
		"https://[fe80::1]/p", "https://100.64.0.1/p", "https://localhost/p", "https://printer.local/p", "https://svc.internal/p",
		"https://0.0.0.0/p", "https://" + strings.Repeat("a", 2100) + ".example/p",
	} {
		if _, err := open.CheckEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	list := Policy{AllowHosts: []string{"push.example.net", "*.relay.example"}}
	for _, ok := range []string{"https://push.example.net/p", "https://a.relay.example/p"} {
		if _, err := list.CheckEndpoint(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://fcm.googleapis.com/x", "https://relay.example/p", "https://evilrelay.example/p", "https://push.example.net.evil.test/p"} {
		if _, err := list.CheckEndpoint(bad); err == nil {
			t.Errorf("%q accepted by an allow list that does not name it", bad)
		}
	}
	// Naming a private host allows it (a site's own relay), on any port.
	own := Policy{AllowHosts: []string{"10.1.2.3", "relay.lan.internal"}}
	for _, ok := range []string{"https://10.1.2.3/p", "https://relay.lan.internal/p", "https://relay.lan.internal:8443/p"} {
		if _, err := own.CheckEndpoint(ok); err != nil {
			t.Errorf("%s refused though named: %v", ok, err)
		}
	}
}

// Exactly the row Add made goes when its save fails, and what it displaced
// comes back; a row a load put there meanwhile is left alone.
func TestUndo(t *testing.T) {
	g, _ := newReg(t)
	old, _, _ := g.Add("user:ann", nil, "c", "h", testSub(1), t0)
	row, ch, _ := g.Add("user:ann", nil, "c", "h", testSub(1), t0.Add(time.Second)) // replaces
	g.Undo(row, ch)
	if got := g.Of("user:ann"); len(got) != 1 || got[0].Sealed != old.Sealed {
		t.Fatalf("undo of a replace: %+v", got)
	}
	taken, _, _ := g.Add("user:ann", nil, "c", "h", testSub(2), t0)
	row, ch, _ = g.Add("user:bob", nil, "c", "h", testSub(2), t0.Add(time.Second)) // takes it
	g.Undo(row, ch)
	if len(g.Of("user:bob")) != 0 || len(g.Of("user:ann")) != 2 {
		t.Fatalf("undo of a takeover: bob %d ann %d", len(g.Of("user:bob")), len(g.Of("user:ann")))
	}
	_ = taken
	// A store load landed meanwhile: the undo does not put the old set back over it.
	row, ch, _ = g.Add("user:cy", nil, "c", "h", testSub(3), t0)
	g.Load([]Row{{ID: "loaded", Principal: "user:dee", Sealed: "x", Key: g.KeyFingerprint()}})
	g.Undo(row, ch)
	if rows := g.Rows(); len(rows) != 1 || rows[0].ID != "loaded" {
		t.Errorf("undo disturbed a load: %+v", rows)
	}
}

// Cred and groups are bound into the seal: editing them beside it makes
// the row fail to open, not stand under someone else's credential.
func TestSealBindsCredAndGroups(t *testing.T) {
	g, _ := newReg(t)
	row, _, _ := g.Add("oidc:ann", []string{"ops"}, "pin1", "h", testSub(1), t0)
	for name, mut := range map[string]func(*Row){
		"cred":   func(r *Row) { r.Cred = "pin2" },
		"groups": func(r *Row) { r.Groups = []string{"ops", "admins"} },
	} {
		r := row
		mut(&r)
		if _, err := g.Open(r); err != ErrSealed {
			t.Errorf("%s edited: %v", name, err)
		}
	}
	if _, err := g.Open(row); err != nil {
		t.Error(err)
	}
}

func TestRowIDIs128Bits(t *testing.T) {
	if n := len(EndpointID("https://push.example.net/p/1")); n != 32 {
		t.Errorf("id has %d hex digits", n)
	}
}

// A prune takes exactly the rows it judged: one re-made since (the same
// endpoint subscribed again) is not its.
func TestRemoveRowsIsExact(t *testing.T) {
	g, _ := newReg(t)
	old, _, _ := g.Add("user:ann", nil, "c", "h", testSub(1), t0)
	fresh, _, _ := g.Add("user:ann", nil, "c2", "h", testSub(1), t0.Add(time.Second)) // same endpoint, re-made
	g.RemoveRows([]Row{old})
	if rows := g.Of("user:ann"); len(rows) != 1 || rows[0].Sealed != fresh.Sealed {
		t.Fatalf("the prune took the re-made row: %+v", rows)
	}
}
