package server

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
	"github.com/joyautomation/nautilus/runtime"
)

// fakeIdentity is an Identity over a fixed set of users: name to
// password, and the tags each may write. It counts sign-ins, can be told
// to stop holding, and keeps what it is given to record.
type fakeIdentity struct {
	passwords map[string]string
	writes    map[string][]string // principal ("user:op", "opcua-cert:scada") to the tags (roots) it may write
	certs     map[string]string   // thumbprint or subject to a certificate's name
	gate      chan struct{}       // when set, each SignIn waits for it to close
	revoked   atomic.Bool

	mu      sync.Mutex
	signIns []string // "name from"
	entries []audit.Entry
}

func newFakeIdentity() *fakeIdentity {
	return &fakeIdentity{
		passwords: map[string]string{"op": "op-pass", "eng": "eng-pass"},
		writes:    map[string][]string{"user:op": {"Speed", "P101"}, "user:eng": {"*"}, "opcua-cert:scada": {"Speed"}},
		certs:     map[string]string{},
	}
}

func (f *fakeIdentity) SignsIn() bool { return true }

func (f *fakeIdentity) SignIn(ctx context.Context, from, name, password string, detail map[string]any) (string, string, error) {
	f.mu.Lock()
	f.signIns = append(f.signIns, name+" "+from)
	f.mu.Unlock()
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	if pw, ok := f.passwords[name]; !ok || pw != password {
		f.Record(audit.Entry{Action: "auth.signin", Result: audit.ResultRefused, Target: "user:" + name, From: from, Detail: detail})
		return "", "", errors.New("refused")
	}
	f.Record(audit.Entry{Action: "auth.signin", Result: audit.ResultOK, User: "user:" + name, From: from, Detail: detail})
	return name, "cred-" + name, nil
}

func (f *fakeIdentity) Holds(user, cred string) bool {
	return !f.revoked.Load() && cred == "cred-"+user
}

func (f *fakeIdentity) OPCUACert(thumbprint, subject string) (string, bool) {
	if n, ok := f.certs[thumbprint]; ok {
		return n, true
	}
	n, ok := f.certs[subject]
	return n, ok
}

func (f *fakeIdentity) May(p auth.Principal, perm, target string) bool {
	if perm != "write" {
		return false
	}
	root := target
	if i := strings.IndexAny(root, ".["); i >= 0 {
		root = root[:i]
	}
	for _, t := range f.writes[p.String()] {
		if t == "*" || strings.EqualFold(t, root) {
			return true
		}
	}
	return false
}

func (f *fakeIdentity) Record(e audit.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
}

func (f *fakeIdentity) recorded(action string) []audit.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []audit.Entry
	for _, e := range f.entries {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeIdentity) signInCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.signIns)
}

// userFixture is a secure, writable server with an Identity, and a
// trusted client certificate.
type userFixture struct {
	rt  *runtime.Runtime
	s   *Server
	id  *fakeIdentity
	dir string // the PKI
	der []byte
	key *rsa.PrivateKey
}

func startUsers(t *testing.T, id *fakeIdentity, o Options) *userFixture {
	t.Helper()
	f := &userFixture{rt: writeRuntime(t), id: id}
	dir := filepath.Join(t.TempDir(), "pki")
	o.Endpoint, o.Name, o.Sweep, o.PKI, o.Writable = "localhost:0", "lift-station", 20*time.Millisecond, dir, writePatterns
	o.Logger = slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	s, err := New(f.rt, o)
	if err != nil {
		t.Fatal(err)
	}
	if id != nil {
		s.SetIdentity(id)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f.s, f.dir = s, dir
	f.der, f.key = clientCert(t, writerURI, time.Now().Add(-time.Hour), 24*time.Hour)
	trust(t, dir, f.der)
	return f
}

// connectUser connects on Basic256Sha256/SignAndEncrypt with the trusted
// certificate, as name with password ("" connects anonymously).
func (f *userFixture) connectUser(t *testing.T, name, password string) (*opcua.Client, error) {
	t.Helper()
	ep := endpointFor(t, f.s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)
	if name == "" {
		c, err := secureConnect(f.s, ep, f.der, f.key, writerURI)
		if err == nil {
			t.Cleanup(func() { closeClient(c) })
		}
		return c, err
	}
	c, err := opcua.NewClient(f.s.URL(),
		opcua.AuthUsername(name, password),
		opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeUserName),
		opcua.Certificate(f.der), opcua.PrivateKey(f.key), opcua.ApplicationURI(writerURI),
		opcua.AutoReconnect(false), opcua.DialTimeout(3*time.Second), opcua.RequestTimeout(5*time.Second))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := gopcuatest.Connect(ctx, c); err != nil {
		return nil, err
	}
	t.Cleanup(func() { closeClient(c) })
	return c, nil
}

func userTokens(ep *ua.EndpointDescription) []string {
	var out []string
	for _, p := range ep.UserIdentityTokens {
		if p.TokenType == ua.UserTokenTypeUserName {
			out = append(out, p.PolicyID+" "+p.SecurityPolicyURI)
		}
	}
	return out
}

// With users to sign in, every secure endpoint offers a UserName token
// encrypted under a secure policy, and the None endpoint offers none: a
// session there carries no signature binding its requests to the user.
// Without an Identity nothing but anonymous is offered, as before.
func TestUserNameIsOfferedOnlyOnSecureEndpoints(t *testing.T) {
	f := startUsers(t, newFakeIdentity(), Options{})
	eps, err := opcua.GetEndpoints(context.Background(), f.s.URL())
	if err != nil {
		t.Fatal(err)
	}
	secure := 0
	for _, ep := range eps {
		toks := userTokens(ep)
		if ep.SecurityPolicyURI == ua.SecurityPolicyURINone {
			if len(toks) > 0 {
				t.Errorf("the None endpoint %s offers UserName: %v", ep.EndpointURL, toks)
			}
			continue
		}
		secure++
		if len(toks) == 0 {
			t.Errorf("secure endpoint %s %s offers no UserName token", ep.EndpointURL, ep.SecurityPolicyURI)
		}
		for _, tok := range toks {
			if strings.HasSuffix(tok, " ") || strings.HasSuffix(tok, ua.SecurityPolicyURINone) {
				t.Errorf("%s: a UserName token not encrypted under a secure policy: %q", ep.SecurityPolicyURI, tok)
			}
		}
	}
	if secure == 0 {
		t.Fatal("no secure endpoints")
	}
	plain := startUsers(t, nil, Options{})
	eps, err = opcua.GetEndpoints(context.Background(), plain.s.URL())
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if toks := userTokens(ep); len(toks) > 0 {
			t.Errorf("without users, %s offers UserName: %v", ep.SecurityPolicyURI, toks)
		}
	}
}

// A user session writes what opcua.writable opens AND the user's roles
// allow, refused Bad_UserAccessDenied elsewhere; UserAccessLevel says so
// per node; and the journal names the user, from the certificate's
// thumbprint, with each value before and after.
func TestUserSessionWritesWhereItsRolesAllow(t *testing.T) {
	id := newFakeIdentity()
	f := startUsers(t, id, Options{})
	c, err := f.connectUser(t, "op", "op-pass")
	if err != nil {
		t.Fatal(err)
	}
	if got := write(t, c, wv("Speed", 42.0)); got[0] != ua.StatusGood {
		t.Errorf("op writes Speed: %v", got[0])
	}
	if v := stored(t, f.rt, "Speed"); v != 42.0 {
		t.Errorf("Speed = %v", v)
	}
	// Hi is writable to the certificate but not on op's areas; Closed is
	// not writable at all, and says so (as to any writer).
	got := write(t, c, wv("Hi", 1.0), wv("Closed", 1.0))
	if got[0] != ua.StatusBadUserAccessDenied || got[1] != ua.StatusBadNotWritable {
		t.Errorf("op writes Hi, Closed: %v, want BadUserAccessDenied, BadNotWritable", got)
	}
	if v := stored(t, f.rt, "Hi"); v != 0.0 {
		t.Errorf("Hi changed to %v", v)
	}
	for _, c2 := range []struct {
		addr  string
		write bool
	}{{"Speed", true}, {"Hi", false}, {"Closed", false}} {
		resp, err := c.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: ua.NewStringNodeID(1, c2.addr), AttributeID: ua.AttributeIDUserAccessLevel}}})
		if err != nil {
			t.Fatal(err)
		}
		lvl, _ := resp.Results[0].Value.Value().(uint8)
		if w := lvl&uint8(ua.AccessLevelTypeCurrentWrite) != 0; w != c2.write {
			t.Errorf("op's UserAccessLevel on %s: %d, want write %v", c2.addr, lvl, c2.write)
		}
	}
	thumb := Thumbprint(f.der)[:16]
	// One entry per item, each naming its tag, so the journal's target
	// filter finds who wrote it.
	writes := id.recorded("tag.write")
	if len(writes) != 3 {
		t.Fatalf("%d tag.write entries, want 3 (one per item): %+v", len(writes), writes)
	}
	ok, hi, closed := writes[0], writes[1], writes[2]
	if ok.Result != audit.ResultOK || ok.User != "user:op" || ok.Target != "Speed" || ok.From != thumb || ok.Before != 0.0 || ok.After != 42.0 {
		t.Errorf("the landed write's entry: %+v", ok)
	}
	if hi.Result != audit.ResultRefused || hi.User != "user:op" || hi.From != thumb || hi.Target != "Hi" || hi.Detail["status"] != "BadUserAccessDenied" ||
		!strings.Contains(hi.Detail["reason"].(string), "roles do not allow") {
		t.Errorf("the refused Hi's entry: %+v", hi)
	}
	if closed.Result != audit.ResultRefused || closed.Target != "Closed" || closed.Detail["status"] != "BadNotWritable" {
		t.Errorf("the refused Closed's entry: %+v", closed)
	}
	if si := id.recorded("auth.signin"); len(si) != 1 || si[0].Result != audit.ResultOK || si[0].Detail["thumbprint"] != thumb || si[0].From != "127.0.0.1" {
		t.Errorf("the sign-in: %+v", si)
	}
	// An engineer writes Hi.
	ce, err := f.connectUser(t, "eng", "eng-pass")
	if err != nil {
		t.Fatal(err)
	}
	if got := write(t, ce, wv("Hi", 5.0)); got[0] != ua.StatusGood {
		t.Errorf("eng writes Hi: %v", got[0])
	}
}

// A wrong password, or a name nobody has, refuses the activation with
// Bad_UserAccessDenied (the same answer for both), after the Identity has
// checked it.
func TestWrongPasswordRefusesTheSession(t *testing.T) {
	id := newFakeIdentity()
	f := startUsers(t, id, Options{})
	for _, c := range []struct{ name, pw string }{{"op", "guess"}, {"nobody", "op-pass"}} {
		_, err := f.connectUser(t, c.name, c.pw)
		if !errors.Is(err, ua.StatusBadUserAccessDenied) {
			t.Errorf("%s/%s: %v, want BadUserAccessDenied", c.name, c.pw, err)
		}
	}
	if n := id.signInCount(); n != 2 {
		t.Errorf("%d sign-ins checked, want 2", n)
	}
}

// A UserName token on the None channel is refused before any password is
// checked: Bad_IdentityTokenRejected, and the Identity never asked.
func TestUserNameOnTheNoneChannelIsRefused(t *testing.T) {
	id := newFakeIdentity()
	f := startUsers(t, id, Options{})
	c, err := opcua.NewClient(f.s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.SecurityPolicy(ua.SecurityPolicyURINone),
		opcua.AuthUsername("op", "op-pass"), opcua.AuthPolicyID("username_basic256sha256"), opcua.AutoReconnect(false),
		opcua.DialTimeout(3*time.Second), opcua.RequestTimeout(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = gopcuatest.Connect(ctx, c)
	if err == nil {
		closeClient(c)
	}
	if !errors.Is(err, ua.StatusBadIdentityTokenRejected) {
		t.Errorf("UserName on None: %v, want BadIdentityTokenRejected", err)
	}
	if n := id.signInCount(); n != 0 {
		t.Errorf("the password was checked %d times on an unsigned channel", n)
	}
}

// A user who no longer holds their sign-in (removed, disabled, a new
// password) writes nothing from that moment, on the session they already
// have: Bad_UserAccessDenied, and UserAccessLevel read-only.
func TestRevokedUserStopsWriting(t *testing.T) {
	id := newFakeIdentity()
	f := startUsers(t, id, Options{})
	c, err := f.connectUser(t, "op", "op-pass")
	if err != nil {
		t.Fatal(err)
	}
	if got := write(t, c, wv("Speed", 1.0)); got[0] != ua.StatusGood {
		t.Fatalf("before: %v", got[0])
	}
	id.revoked.Store(true)
	if got := write(t, c, wv("Speed", 2.0)); got[0] != ua.StatusBadUserAccessDenied {
		t.Errorf("after the revocation: %v, want BadUserAccessDenied", got[0])
	}
	if v := stored(t, f.rt, "Speed"); v != 1.0 {
		t.Errorf("Speed = %v after the revoked write", v)
	}
	resp, err := c.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: ua.NewStringNodeID(1, "Speed"), AttributeID: ua.AttributeIDUserAccessLevel}}})
	if err != nil {
		t.Fatal(err)
	}
	if lvl, _ := resp.Results[0].Value.Value().(uint8); lvl&uint8(ua.AccessLevelTypeCurrentWrite) != 0 {
		t.Errorf("a revoked user's UserAccessLevel: %d", lvl)
	}
	es := id.recorded("tag.write")
	if last := es[len(es)-1]; last.Result != audit.ResultRefused || last.Target != "Speed" || !strings.Contains(last.Detail["reason"].(string), "no longer holds") {
		t.Errorf("the revoked write's entry: %+v", last)
	}
}

// Under an auth: block (an Identity), a session without a user writes
// only as a certificate the block maps to roles (the maintainer's
// decision, 2026-11-20): a mapped certificate writes in its areas, is
// refused outside them, and is journalled by its name; an unmapped one is
// refused with a reason naming UserName; an anonymous session under
// anonymous-writes writes nothing. Without an Identity (no auth: block) a
// trusted certificate writes what opcua.writable opens, as before.
func TestCertificatesWriteOnlyWhenMapped(t *testing.T) {
	// Unmapped: read-only, told to sign in with a user name.
	id := newFakeIdentity()
	f := startUsers(t, id, Options{AnonymousWrites: true})
	cert, err := f.connectUser(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := write(t, cert, wv("Speed", 3.0)); got[0] != ua.StatusBadUserAccessDenied {
		t.Errorf("an unmapped certificate writes Speed: %v, want BadUserAccessDenied", got[0])
	}
	es := id.recorded("tag.write")
	if len(es) != 1 || es[0].User != "anonymous" || !strings.Contains(es[0].Detail["reason"].(string), "UserName") ||
		!strings.Contains(es[0].Detail["reason"].(string), "opcua-certificates") {
		t.Errorf("the unmapped certificate's refusal: %+v", es)
	}
	anon, err := opcua.NewClient(f.s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := gopcuatest.Connect(context.Background(), anon); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeClient(anon) })
	if got := write(t, anon, wv("Lo", 3.0)); got[0] != ua.StatusBadUserAccessDenied {
		t.Errorf("anonymous-writes under an Identity writes Lo: %v", got[0])
	}
	// A user on the same certificate still writes.
	user, err := f.connectUser(t, "op", "op-pass")
	if err != nil {
		t.Fatal(err)
	}
	if got := write(t, user, wv("Speed", 4.0)); got[0] != ua.StatusGood {
		t.Errorf("a user session writes Speed: %v", got[0])
	}

	// Mapped, by thumbprint and by subject: writes in its areas only, as
	// opcua-cert:scada.
	for _, by := range []string{"thumbprint", "subject"} {
		id := newFakeIdentity()
		f := startUsers(t, id, Options{})
		leaf, _ := x509.ParseCertificate(f.der)
		key := Thumbprint(f.der)
		if by == "subject" {
			key = leaf.Subject.String()
		}
		id.certs[key] = "scada"
		c, err := f.connectUser(t, "", "")
		if err != nil {
			t.Fatal(err)
		}
		got := write(t, c, wv("Speed", 5.0), wv("Hi", 5.0))
		if got[0] != ua.StatusGood || got[1] != ua.StatusBadUserAccessDenied {
			t.Errorf("by %s: the mapped certificate writes Speed, Hi: %v, want Good, BadUserAccessDenied", by, got)
		}
		es := id.recorded("tag.write")
		if len(es) != 2 || es[0].User != "opcua-cert:scada" || es[0].Target != "Speed" || es[0].Result != audit.ResultOK ||
			es[1].User != "opcua-cert:scada" || es[1].Target != "Hi" || es[1].Result != audit.ResultRefused {
			t.Errorf("by %s: the journal: %+v", by, es)
		}
		resp, err := c.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{
			{NodeID: ua.NewStringNodeID(1, "Speed"), AttributeID: ua.AttributeIDUserAccessLevel},
			{NodeID: ua.NewStringNodeID(1, "Hi"), AttributeID: ua.AttributeIDUserAccessLevel}}})
		if err != nil {
			t.Fatal(err)
		}
		sp, _ := resp.Results[0].Value.Value().(uint8)
		hi, _ := resp.Results[1].Value.Value().(uint8)
		if sp&uint8(ua.AccessLevelTypeCurrentWrite) == 0 || hi&uint8(ua.AccessLevelTypeCurrentWrite) != 0 {
			t.Errorf("by %s: UserAccessLevel Speed %d, Hi %d", by, sp, hi)
		}
	}

	// No auth: block: the certificate writes, as before.
	plain := startUsers(t, nil, Options{})
	pc, err := plain.connectUser(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := write(t, pc, wv("Hi", 6.0)); got[0] != ua.StatusGood {
		t.Errorf("no auth: block, a trusted certificate writes Hi: %v", got[0])
	}
}

// The password check runs off the server's message loop: while one
// client's sign-in is held inside the check, another client's read is
// answered (with the check on the loop, the read would wait behind it).
func TestUserNameCheckDoesNotStallOtherClients(t *testing.T) {
	id := newFakeIdentity()
	id.gate = make(chan struct{})
	f := startUsers(t, id, Options{})
	other, err := f.connectUser(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ep := endpointFor(t, f.s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)
	slow, err := opcua.NewClient(f.s.URL(),
		opcua.AuthUsername("op", "op-pass"), opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeUserName),
		opcua.Certificate(f.der), opcua.PrivateKey(f.key), opcua.ApplicationURI(writerURI),
		opcua.AutoReconnect(false), opcua.DialTimeout(3*time.Second), opcua.RequestTimeout(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		done <- gopcuatest.Connect(ctx, slow)
	}()
	// Wait until the check is under way and held.
	deadline := time.Now().Add(10 * time.Second)
	for id.signInCount() == 0 {
		if time.Now().After(deadline) {
			close(id.gate)
			t.Fatal("the sign-in never reached the check")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, rerr := other.Read(ctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: ua.NewStringNodeID(1, "Speed"), AttributeID: ua.AttributeIDValue}}})
	cancel()
	close(id.gate)
	if rerr != nil {
		t.Errorf("a read while another client's sign-in is held: %v", rerr)
	}
	if err := <-done; err != nil {
		t.Errorf("the held sign-in, once let go: %v", err)
	} else {
		closeClient(slow)
	}
}

// The UserName check itself refuses a channel that is not a writer's
// (no SignAndEncrypt from a trusted certificate) before asking for the
// password, and journals why: the session service already refuses an
// unsigned channel, and this holds even if it did not.
func TestCheckUserNeedsAWritersChannel(t *testing.T) {
	id := newFakeIdentity()
	w := &writeAccess{id: id}
	if _, _, code := w.checkUser(context.Background(), nil, "op", []byte("op-pass")); code != ua.StatusBadIdentityTokenRejected {
		t.Errorf("a non-writer's channel: %v, want BadIdentityTokenRejected", code)
	}
	if n := id.signInCount(); n != 0 {
		t.Errorf("the password was checked %d times", n)
	}
	if es := id.recorded("auth.signin"); len(es) != 1 || es[0].Result != audit.ResultRefused || es[0].Target != "user:op" || es[0].Detail["reason"] == nil {
		t.Errorf("the refusal's entry: %+v", es)
	}
	if _, _, code := (&writeAccess{}).checkUser(context.Background(), nil, "op", nil); code != ua.StatusBadIdentityTokenRejected {
		t.Errorf("no identity: %v", code)
	}
}

// UserName tokens from asyncua (testdata/foreign/asyncua_username.py): a
// second stack's password encryption under each secure policy is read,
// the user writes on their areas only, and a wrong password is
// Bad_UserAccessDenied. Skipped without NAUTILUS_OPCUA_PYTHON (CI sets it).
func TestForeignAsyncuaUserName(t *testing.T) {
	python := os.Getenv("NAUTILUS_OPCUA_PYTHON")
	if python == "" {
		t.Skip("set NAUTILUS_OPCUA_PYTHON=/path/to/python (with asyncua installed) to run the foreign-stack test")
	}
	id := newFakeIdentity()
	f := startUsers(t, id, Options{})
	const uri = "urn:test:asyncua"
	der, key := clientCert(t, uri, time.Now().Add(-time.Hour), 24*time.Hour)
	trust(t, f.dir, der)
	tmp := t.TempDir()
	certFile, keyFile := filepath.Join(tmp, "client.der"), filepath.Join(tmp, "client-key.pem")
	os.WriteFile(certFile, der, 0o600)
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0o600)
	script, _ := filepath.Abs(filepath.Join("testdata", "foreign", "asyncua_username.py"))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, python, "-I", script, f.s.URL(), certFile, keyFile, uri, "op", "op-pass")
	cmd.Stderr = &stderr // asyncua's warnings
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("asyncua: %v\n%s%s", err, out, stderr.String())
	}
	var steps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			t.Fatalf("asyncua printed %q", line)
		}
		steps = append(steps, m)
	}
	if len(steps) != 4 {
		t.Fatalf("asyncua printed %d steps:\n%s", len(steps), out)
	}
	for _, m := range steps[:3] {
		if m["ok"] != true || m["speed"] != "Good" || m["hi"] != "BadUserAccessDenied" || m["userAccessLevel"] != float64(3) {
			t.Errorf("%v as op: %v, want Speed Good, Hi BadUserAccessDenied, UserAccessLevel 3", m["policy"], m)
		}
	}
	if w := steps[3]; w["ok"] == true || !strings.Contains(fmt.Sprint(w["error"]), "BadUserAccessDenied") {
		t.Errorf("a wrong password: %v, want BadUserAccessDenied", w)
	}
	if v := stored(t, f.rt, "Speed"); v != 12.5 {
		t.Errorf("Speed = %v", v)
	}
}
