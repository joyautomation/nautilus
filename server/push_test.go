package server

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/webpush"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// Web Push subscriptions (#722 slice 2): bound to the person the session
// names, sealed in retain, ended when the person stops standing, paged
// only by a leader.

var pb64 = base64.RawURLEncoding

// The markers a test plants in a subscription, then hunts for everywhere.
const (
	markEndpointPath = "SECRETENDPOINTPATH"
)

func subJSON(n int, extra ...string) (body string, marks []string) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	au := make([]byte, 16)
	rand.Read(au)
	p256, auth := pb64.EncodeToString(ua.PublicKey().Bytes()), pb64.EncodeToString(au)
	ep := fmt.Sprintf("https://push.example.net/%s/%d", markEndpointPath, n)
	b, _ := json.Marshal(map[string]any{"endpoint": ep, "keys": map[string]string{"p256dh": p256, "auth": auth}})
	return string(b), []string{markEndpointPath, p256, auth}
}

type pushRig struct {
	toks  map[string]string
	cs    map[string]*http.Cookie
	srv   *Server
	rt    *runtime.Runtime
	store *subStore
	path  string
	key   *webpush.Key
}

func newPushRig(t *testing.T, store *subStore, key *webpush.Key, allow ...string) *pushRig {
	t.Helper()
	if key == nil {
		var err error
		if key, err = webpush.NewKey(); err != nil {
			t.Fatal(err)
		}
	}
	rt, err := runtime.New(runtime.Options{
		Program: testProgram, Driver: nio.NewMemory(), Retain: store,
		Inputs: []string{"Level"}, Outputs: []string{"Out"},
		Seed: nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.LoadRetained()
	srv, toks, path := patServerRT(t, rt, Options{Push: &PushOptions{Key: key, Subject: "mailto:ops@example.com", AllowHosts: allow}})
	rt.Scan()
	// The saver (SaveRetained answers from it) runs inside Run.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &pushRig{toks, map[string]*http.Cookie{}, srv, rt, store, path, key}
}

func (p *pushRig) subscribe(t *testing.T, who string, body string) *httptest.ResponseRecorder {
	t.Helper()
	c := p.cs[who]
	if c == nil {
		c = signIn(t, p.srv, who) // once: the sign-in throttle
		p.cs[who] = c
	}
	return call(t, p.srv, "POST", "/api/push/subscribe", body, withCookie(c.Value), ownOrigin, asJSON)
}

func journalText(s *Server) string {
	b, _ := json.Marshal(s.audit.ring.Recent())
	return string(b)
}

func storeText(st *subStore) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, _ := json.Marshal(st.st)
	return string(b)
}

func TestPushSubscribeBindsToTheSessionAndKeepsNoSecretAnywhere(t *testing.T) {
	logs := captureLog(t)
	store := &subStore{}
	rig := newPushRig(t, store, nil)
	srv := rig.srv
	body, marks := subJSON(1)

	rec := rig.subscribe(t, "jsmith", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	var made pushRowView
	json.Unmarshal(rec.Body.Bytes(), &made)
	if made.ID == "" || made.Host != "push.example.net" {
		t.Fatalf("answer %s", rec.Body)
	}
	// The principal is the session's: the body names none, and a body that
	// tries is refused as an unknown field.
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	m["principal"] = "user:akhan"
	forged, _ := json.Marshal(m)
	if rec := rig.subscribe(t, "jsmith", string(forged)); rec.Code != http.StatusBadRequest {
		t.Errorf("a body naming a principal: %d", rec.Code)
	}
	if got := srv.push.reg.Of("user:jsmith"); len(got) != 1 || got[0].ID != made.ID {
		t.Fatalf("rows %+v", got)
	}
	if len(srv.push.reg.Of("user:akhan")) != 0 {
		t.Error("akhan holds a subscription jsmith made")
	}

	c := signIn(t, srv, "jsmith")
	list := call(t, srv, "GET", "/api/push/subscriptions", "", withCookie(c.Value)).Body.String()
	key := call(t, srv, "GET", "/api/push/key", "", withCookie(c.Value))
	if key.Code != 200 || !strings.Contains(key.Body.String(), rig.key.Public()) {
		t.Errorf("key: %d %s", key.Code, key.Body)
	}
	ak := signIn(t, srv, "akhan")
	health := call(t, srv, "GET", "/api/push/health", "", withCookie(ak.Value)).Body.String()
	if !strings.Contains(list, made.ID) || !strings.Contains(health, `"subscriptions":1`) {
		t.Errorf("list %s health %s", list, health)
	}
	if got := call(t, srv, "GET", "/api/push/subscriptions", "", withCookie(ak.Value)).Body.String(); strings.Contains(got, made.ID) {
		t.Errorf("akhan lists jsmith's subscription: %s", got)
	}
	// jsmith (operator) may not read the health counts: the config permission.
	if rec := call(t, srv, "GET", "/api/push/health", "", withCookie(c.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("health for an operator: %d", rec.Code)
	}

	// The secret's every place: the answers, the journal, the log, what
	// retain holds (and so a ConfigMap and the 0644 file), the state API.
	state := call(t, srv, "GET", "/api/state", "", withCookie(c.Value)).Body.String()
	if err := rig.rt.SaveRetained(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, where := range []struct{ name, text string }{
		{"answer", rec.Body.String()}, {"list", list}, {"key", key.Body.String()}, {"health", health},
		{"journal", journalText(srv)}, {"log", logs.String()}, {"retain", storeText(store)}, {"state", state},
	} {
		for _, mk := range marks {
			if strings.Contains(where.text, mk) {
				t.Errorf("%s holds %q", where.name, mk)
			}
		}
	}
	if !strings.Contains(storeText(store), `"sealed"`) {
		t.Errorf("retain does not hold the row: %s", storeText(store))
	}
	// One journal entry, by the person, naming the subscription not the endpoint.
	var ents []audit.Entry
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "push.subscribe" && e.Result == audit.ResultOK {
			ents = append(ents, e)
		}
	}
	if len(ents) != 1 || ents[0].User != "user:jsmith" || ents[0].Target != "push:"+made.ID || ents[0].Result != audit.ResultOK {
		t.Errorf("journal %+v", ents)
	}
}

func TestPushSubscribeRefusals(t *testing.T) {
	rig := newPushRig(t, &subStore{}, nil)
	srv := rig.srv
	good, _ := subJSON(1)
	jc := signIn(t, srv, "jsmith")
	post := func(body string, mod ...func(*http.Request)) int {
		return call(t, srv, "POST", "/api/push/subscribe", body, append([]func(*http.Request){asJSON}, mod...)...).Code
	}
	if c := post(good, ownOrigin); c != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", c)
	}
	if c := post(good, withCookie(jc.Value)); c != http.StatusForbidden {
		t.Errorf("a cookie from no page of ours: %d", c)
	}
	for name, body := range map[string]string{
		"http":       strings.Replace(good, "https://", "http://", 1),
		"loopback":   strings.Replace(good, "push.example.net", "127.0.0.1", 1),
		"private":    strings.Replace(good, "push.example.net", "192.168.1.9", 1),
		"link-local": strings.Replace(good, "push.example.net", "169.254.169.254", 1),
		"userinfo":   strings.Replace(good, "push.example.net", "u:p@push.example.net", 1),
		"no keys":    `{"endpoint":"https://push.example.net/x"}`,
		"bad p256dh": strings.Replace(good, `"p256dh":"`, `"p256dh":"AAA`, 1),
		"not json":   `endpoint=https://push.example.net/x`,
		"empty":      ``,
		"big":        `{"endpoint":"https://push.example.net/` + strings.Repeat("a", 9000) + `"}`,
	} {
		if c := post(body, withCookie(jc.Value), ownOrigin); c != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, c)
		}
	}
	if n := len(srv.push.reg.Rows()); n != 0 {
		t.Errorf("%d rows kept from refused requests", n)
	}
}

// Only a person has a phone: a service token or NAUTILUS_TOKEN does not.
func TestPushIsForPeopleOnly(t *testing.T) {
	rig := newPushRig(t, &subStore{}, nil)
	srv := rig.srv
	good, _ := subJSON(1)
	srv.authToken = "root-token"
	for name, mod := range map[string]func(*http.Request){
		"NAUTILUS_TOKEN": func(r *http.Request) { r.Header.Set("Authorization", "Bearer root-token") },
	} {
		rec := call(t, srv, "POST", "/api/push/subscribe", good, asJSON, mod)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "sign in as a user") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// A person's personal access token is the person: it may subscribe.
	rec := call(t, srv, "POST", "/api/push/subscribe", good, asJSON, bearer(rig.toks["jsmith"]))
	if rec.Code != http.StatusCreated || len(srv.push.reg.Of("user:jsmith")) != 1 {
		t.Errorf("jsmith's token: %d %s", rec.Code, rec.Body)
	}
	// A user the auth: block grants nothing has no standing to be paged.
	rec = call(t, srv, "POST", "/api/push/subscribe", good, asJSON, bearer(rig.toks["ghost"]))
	if rec.Code == http.StatusCreated && len(srv.push.reg.Of("user:ghost")) == 1 {
		srv.push.sweep(context.Background())
		if len(srv.push.reg.Of("user:ghost")) != 0 {
			t.Error("a user with no grants keeps a subscription")
		}
	}
}

func TestPushEndpointTakenByTheNewestPerson(t *testing.T) {
	rig := newPushRig(t, &subStore{}, nil)
	srv := rig.srv
	body, _ := subJSON(1)
	rig.subscribe(t, "akhan", body)
	if rec := rig.subscribe(t, "jsmith", body); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	if len(srv.push.reg.Of("user:akhan")) != 0 || len(srv.push.reg.Of("user:jsmith")) != 1 {
		t.Fatal("the endpoint is not jsmith's alone")
	}
	var took *audit.Entry
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "push.unsubscribe" && e.User == "system" {
			e := e
			took = &e
		}
	}
	if took == nil || took.Detail["principal"] != "user:akhan" || !strings.Contains(fmt.Sprint(took.Detail["reason"]), "user:jsmith") {
		t.Errorf("the takeover is not journalled: %+v", took)
	}
}

func TestPushCapPerPerson(t *testing.T) {
	rig := newPushRig(t, &subStore{}, nil)
	for i := 1; i <= webpush.MaxPerPrincipal+3; i++ {
		b, _ := subJSON(i)
		if rec := rig.subscribe(t, "jsmith", b); rec.Code != http.StatusCreated {
			t.Fatal(rec.Body)
		}
	}
	if n := len(rig.srv.push.reg.Of("user:jsmith")); n != webpush.MaxPerPrincipal {
		t.Errorf("%d subscriptions, cap %d", n, webpush.MaxPerPrincipal)
	}
}

func TestPushUnsubscribe(t *testing.T) {
	rig := newPushRig(t, &subStore{}, nil)
	srv := rig.srv
	body, _ := subJSON(1)
	var made pushRowView
	json.Unmarshal(rig.subscribe(t, "jsmith", body).Body.Bytes(), &made)
	ak, js := signIn(t, srv, "akhan"), signIn(t, srv, "jsmith")
	del := func(c *http.Cookie, b string) int {
		return call(t, srv, "DELETE", "/api/push/subscribe", b, withCookie(c.Value), ownOrigin, asJSON).Code
	}
	if c := del(ak, `{"id":"`+made.ID+`"}`); c != http.StatusNotFound {
		t.Errorf("akhan removing jsmith's: %d", c)
	}
	if c := del(js, `{}`); c != http.StatusBadRequest {
		t.Errorf("naming nothing: %d", c)
	}
	if c := del(js, `{"id":"`+made.ID+`"}`); c != http.StatusNoContent {
		t.Errorf("own: %d", c)
	}
	if c := del(js, `{"id":"`+made.ID+`"}`); c != http.StatusNotFound {
		t.Errorf("twice: %d", c)
	}
	body2, _ := subJSON(2)
	rig.subscribe(t, "jsmith", body2)
	var b2 struct{ Endpoint string }
	json.Unmarshal([]byte(body2), &b2)
	if c := del(js, `{"endpoint":"`+b2.Endpoint+`"}`); c != http.StatusNoContent {
		t.Errorf("by endpoint: %d", c)
	}
	if strings.Contains(storeText(rig.store), `"sealed"`) {
		t.Errorf("a removed subscription is still retained: %s", storeText(rig.store))
	}
}

// An answer is given only once the store holds the row.
func TestPushSubscribeIsDurableBeforeItAnswers(t *testing.T) {
	store := &subStore{}
	rig := newPushRig(t, store, nil)
	body, _ := subJSON(1)
	rec := rig.subscribe(t, "jsmith", body)
	if rec.Code != http.StatusCreated || !strings.Contains(storeText(store), `"sealed"`) {
		t.Fatalf("%d, store %s", rec.Code, storeText(store))
	}
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()
	body2, _ := subJSON(2)
	rec = rig.subscribe(t, "jsmith", body2)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a store that cannot be written: %d", rec.Code)
	}
	if n := len(rig.srv.push.reg.Of("user:jsmith")); n != 1 {
		t.Errorf("%d rows after a subscription that was refused", n)
	}
}

// A restart or a takeover loads the rows back; another key keeps them and
// says so, and never drops them.
func TestPushSurvivesARestartAndAnotherKeyKeepsTheRows(t *testing.T) {
	store := &subStore{}
	rig := newPushRig(t, store, nil)
	body, _ := subJSON(1)
	rig.subscribe(t, "jsmith", body)
	if err := rig.rt.SaveRetained(context.Background()); err != nil {
		t.Fatal(err)
	}

	again := newPushRig(t, store, rig.key)
	if n := len(again.srv.push.reg.Of("user:jsmith")); n != 1 {
		t.Fatalf("the restart holds %d rows, want 1", n)
	}
	if h := again.srv.pushHealth(); h.Sealed != 0 || h.Subscriptions != 1 {
		t.Errorf("same key: %+v", h)
	}

	other := newPushRig(t, store, nil)
	h := other.srv.pushHealth()
	if h.Sealed != 1 || h.Subscriptions != 1 {
		t.Fatalf("another key: %+v, want the row kept and counted sealed", h)
	}
	// Kept in what it saves too: a standby with the wrong key must not
	// wipe the leader's rows.
	if rs := other.srv.push.RetainedPush(); rs == nil || len(rs.Rows) != 1 {
		t.Errorf("RetainedPush %+v", rs)
	}
}

// A person whose password changed, who was removed or disabled, stops
// being paged with no live session to end; signing out does not.
func TestPushEndsWithThePersonNotTheSession(t *testing.T) {
	for _, c := range []struct {
		what string
		do   func(*users.File) error
	}{
		{"password", func(f *users.File) error { return f.SetPassword("jsmith", "new-password") }},
		{"disabled", func(f *users.File) error { return f.SetDisabled("jsmith", true) }},
		{"removed", func(f *users.File) error { delete(f.Users, "jsmith"); return nil }},
	} {
		t.Run(c.what, func(t *testing.T) {
			store := &subStore{}
			rig := newPushRig(t, store, nil)
			srv := rig.srv
			body, _ := subJSON(1)
			js := signIn(t, srv, "jsmith")
			if rec := call(t, srv, "POST", "/api/push/subscribe", body, withCookie(js.Value), ownOrigin, asJSON); rec.Code != 201 {
				t.Fatal(rec.Body)
			}
			// Signing out is not a revocation: the phone keeps its pages.
			if rec := call(t, srv, "POST", "/api/signout", "", withCookie(js.Value), ownOrigin); rec.Code >= 300 {
				t.Fatalf("signout %d %s", rec.Code, rec.Body)
			}
			srv.push.sweep(context.Background())
			if len(srv.push.reg.Of("user:jsmith")) != 1 {
				t.Fatal("signing out ended the subscription")
			}
			// No session is left, nor any request: the change alone ends it.
			if err := users.Update(rig.path, "opsadmin", c.do); err != nil {
				t.Fatal(err)
			}
			srv.users.Reload()
			srv.push.sweep(context.Background())
			if n := len(srv.push.reg.Of("user:jsmith")); n != 0 {
				t.Errorf("%d subscriptions survive a %s change with no live session", n, c.what)
			}
			if strings.Contains(storeText(store), `"sealed"`) {
				t.Errorf("the store still holds it: %s", storeText(store))
			}
			var gone *audit.Entry
			for _, e := range srv.audit.ring.Recent() {
				if e.Action == "push.unsubscribe" && e.User == "system" {
					e := e
					gone = &e
				}
			}
			if gone == nil || gone.Detail["principal"] != "user:jsmith" {
				t.Errorf("the revocation is not journalled: %+v", gone)
			}
		})
	}
}

// Another person's change leaves a subscription alone.
func TestPushStaysWhenSomeoneElseChanges(t *testing.T) {
	rig := newPushRig(t, &subStore{}, nil)
	body, _ := subJSON(1)
	rig.subscribe(t, "jsmith", body)
	if err := users.Update(rig.path, "opsadmin", func(f *users.File) error { return f.SetPassword("akhan", "other-password") }); err != nil {
		t.Fatal(err)
	}
	rig.srv.users.Reload()
	rig.srv.push.sweep(context.Background())
	if len(rig.srv.push.reg.Of("user:jsmith")) != 1 {
		t.Error("akhan's password change ended jsmith's subscription")
	}
}

// ---- paging ----

type fakeService struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	got    [][]byte
	hdrs   []http.Header
}

func newFakeService(t *testing.T) *fakeService {
	f := &fakeService{status: 201}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		f.got, f.hdrs = append(f.got, b[:n]), append(f.hdrs, r.Header.Clone())
		w.WriteHeader(f.status)
	}))
	t.Cleanup(f.Close)
	return f
}

func pagedRig(t *testing.T, f *fakeService) (*pushRig, string) {
	t.Helper()
	rig := newPushRig(t, &subStore{}, nil, "127.0.0.1")
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	rig.srv.push.sender.TLS = &tls.Config{RootCAs: pool}
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	au := make([]byte, 16)
	rand.Read(au)
	ep := strings.Replace(f.URL, "https://127.0.0.1", "https://127.0.0.1", 1) + "/p/1"
	b, _ := json.Marshal(map[string]any{"endpoint": ep, "keys": map[string]string{"p256dh": pb64.EncodeToString(ua.PublicKey().Bytes()), "auth": pb64.EncodeToString(au)}})
	if rec := rig.subscribe(t, "jsmith", string(b)); rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return rig, ep
}

func TestPushSendPagesThePersonAndPrunesAGoneSubscription(t *testing.T) {
	f := newFakeService(t)
	rig, ep := pagedRig(t, f)
	srv := rig.srv
	res, err := srv.PushSend(context.Background(), "JSmith", PushMessage{Target: "SP", Topic: "HiTemp", Payload: []byte(`{"alarm":"HiTemp"}`)})
	if err != nil || res.Sent != 1 || res.Failed != 0 || res.NoSubscription {
		t.Fatalf("%+v %v", res, err)
	}
	f.mu.Lock()
	if len(f.got) != 1 || f.hdrs[0].Get("Topic") != "HiTemp" || f.hdrs[0].Get("Content-Encoding") != "aes128gcm" {
		t.Errorf("service saw %d messages, headers %v", len(f.got), f.hdrs)
	}
	f.mu.Unlock()

	// A person with no subscription is reported, not an error.
	res, _ = srv.PushSend(context.Background(), "user:akhan", PushMessage{Target: "SP", Payload: []byte("x")})
	if !res.NoSubscription || res.Sent != 0 {
		t.Errorf("akhan: %+v", res)
	}

	// A failure is a class, never the endpoint.
	f.mu.Lock()
	f.status = 500
	f.mu.Unlock()
	res, _ = srv.PushSend(context.Background(), "user:jsmith", PushMessage{Target: "SP", Payload: []byte("x")})
	if res.Failed != 1 || res.Class != "provider-error" || len(srv.push.reg.Of("user:jsmith")) != 1 {
		t.Errorf("500: %+v", res)
	}

	// 410: the subscription is over: pruned, journalled, saved.
	f.mu.Lock()
	f.status = 410
	f.mu.Unlock()
	res, _ = srv.PushSend(context.Background(), "user:jsmith", PushMessage{Target: "SP", Payload: []byte("x")})
	if res.Pruned != 1 || len(srv.push.reg.Of("user:jsmith")) != 0 {
		t.Errorf("410: %+v", res)
	}
	if strings.Contains(storeText(rig.store), `"sealed"`) {
		t.Error("the pruned row is still retained")
	}
	if text := journalText(srv); strings.Contains(text, ep) || !strings.Contains(text, "HTTP 410") {
		t.Errorf("journal: endpoint present or the prune not named")
	}
}

// A person who no longer stands is not paged, even if nothing has swept.
func TestPushSendChecksStandingNow(t *testing.T) {
	f := newFakeService(t)
	rig, _ := pagedRig(t, f)
	if err := users.Update(rig.path, "opsadmin", func(fl *users.File) error { return fl.SetPassword("jsmith", "reset-password") }); err != nil {
		t.Fatal(err)
	}
	rig.srv.users.Reload()
	res, _ := rig.srv.PushSend(context.Background(), "user:jsmith", PushMessage{Target: "SP", Payload: []byte("x")})
	f.mu.Lock()
	n := len(f.got)
	f.mu.Unlock()
	if n != 0 || res.Sent != 0 || res.Pruned != 1 {
		t.Errorf("a person whose password was reset was paged: %+v, service saw %d", res, n)
	}
}

// Rows under another key are kept and never sent.
func TestPushSendSkipsSealedRows(t *testing.T) {
	f := newFakeService(t)
	rig, _ := pagedRig(t, f)
	other, _ := webpush.NewKey()
	reg2, _ := webpush.NewRegistry(other)
	reg2.Load(rig.srv.push.reg.Rows())
	rig.srv.push.reg = reg2 // this replica holds another push key
	res, _ := rig.srv.PushSend(context.Background(), "user:jsmith", PushMessage{Target: "SP", Payload: []byte("x")})
	f.mu.Lock()
	n := len(f.got)
	f.mu.Unlock()
	if res.Sealed != 1 || res.Sent != 0 || n != 0 || len(reg2.Rows()) != 1 {
		t.Errorf("%+v, service saw %d, rows %d", res, n, len(reg2.Rows()))
	}
}

func TestPushOff(t *testing.T) {
	srv, _, _ := patServer(t)
	js := signIn(t, srv, "jsmith")
	if rec := call(t, srv, "GET", "/api/push/key", "", withCookie(js.Value)); rec.Code != http.StatusNotFound {
		t.Errorf("%d", rec.Code)
	}
	if _, err := srv.PushSend(context.Background(), "user:jsmith", PushMessage{}); err != ErrPushOff {
		t.Errorf("%v", err)
	}
}

// auditSubscription is one valid subscription body for the audit table,
// and the id the journal names it by.
func auditSubscription() (body, id string) {
	body, _ = subJSON(1)
	var b struct{ Endpoint string }
	json.Unmarshal([]byte(body), &b)
	return body, webpush.EndpointID(b.Endpoint)
}

// A standby pages nobody, takes no subscription and revokes nothing: its
// copy of the rows is the leader's to change.
func TestPushStandbyDoesNothing(t *testing.T) {
	key, _ := webpush.NewKey()
	rt, err := runtime.New(runtime.Options{Program: testProgram, Driver: nio.NewMemory(), Coordinator: standbyCoord{}})
	if err != nil {
		t.Fatal(err)
	}
	srv, _, path := patServerRT(t, rt, Options{Push: &PushOptions{Key: key, Subject: "mailto:ops@example.com"}})
	if _, err := srv.PushSend(context.Background(), "jsmith", PushMessage{Target: "SP", Payload: []byte("x")}); err != ErrPushNotLeading {
		t.Errorf("PushSend on a standby: %v", err)
	}
	body, _ := subJSON(1)
	c := signIn(t, srv, "jsmith")
	if rec := call(t, srv, "POST", "/api/push/subscribe", body, withCookie(c.Value), ownOrigin, asJSON); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("subscribe on a standby: %d", rec.Code)
	}
	// Rows the leader kept (loaded here at a takeover) are not swept by a standby.
	row, _, _ := srv.push.reg.Add("user:jsmith", nil, "stale-cred", "push.example.net", webpush.Subscription{}, time.Now())
	_ = row
	users.Update(path, "opsadmin", func(f *users.File) error { return f.SetPassword("jsmith", "new-password") })
	srv.users.Reload()
	srv.push.sweep(context.Background())
	if len(srv.push.reg.Rows()) != 1 {
		t.Error("a standby revoked a subscription")
	}
}
