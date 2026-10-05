package redfish

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joyautomation/nautilus/redfish/mockup"
)

const testPassword = "hunter2-not-in-any-error"

func smallTree() mockup.Tree {
	return mockup.Tree{
		"/redfish/v1":                          json.RawMessage(`{"@odata.id":"/redfish/v1","Systems":{"@odata.id":"/redfish/v1/Systems"}}`),
		"/redfish/v1/Systems":                  json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`),
		"/redfish/v1/Systems/1":                json.RawMessage(`{"@odata.id":"/redfish/v1/Systems/1","Id":"1","PowerState":"On","Actions":{"#ComputerSystem.Reset":{"target":"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"}}}`),
		"/redfish/v1/SessionService/Sessions":  json.RawMessage(`{"Members":[]}`),
		"/redfish/v1/Chassis/1/Thermal":        json.RawMessage(`{"Fans":[{"MemberId":"0","Reading":2100}]}`),
		"/redfish/v1/Chassis/1/Power/Readings": json.RawMessage(`{"W":1}`),
	}
}

func testSource(url string) Source {
	return Source{ID: "NODE1", Host: url, User: "admin", PasswordEnv: "RF_TEST_PASSWORD"}
}

func startMock(t *testing.T, auth mockup.Auth) *mockup.Server {
	t.Helper()
	srv := mockup.New(smallTree(), mockup.Options{Auth: auth, User: "admin", Password: testPassword})
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), testPassword) {
		t.Fatalf("an error carries the password: %v", err)
	}
}

func TestClientSessionLifecycle(t *testing.T) {
	t.Setenv("RF_TEST_PASSWORD", testPassword)
	srv := startMock(t, mockup.AuthSession)
	c, err := NewClient(testSource(srv.URL()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		r, err := c.Get(ctx, "/redfish/v1/Systems/1")
		if err != nil || r.Status != 200 || !strings.Contains(string(r.Body), `"PowerState":"On"`) {
			t.Fatalf("GET: %v %+v", err, r)
		}
	}
	if c.Sessions() != 1 || srv.Logins() != 1 || c.AuthMode() != AuthSession {
		t.Fatalf("one session for three GETs: client %d, server %d, mode %s", c.Sessions(), srv.Logins(), c.AuthMode())
	}
	// A 404 is an answer, not a transport failure.
	if r, err := c.Get(ctx, "/redfish/v1/Nope"); err != nil || r.Status != 404 {
		t.Fatalf("404: %v %+v", err, r)
	}

	// The BMC reboots: every token is gone. The next GET sees 401, logs in
	// again once, and succeeds — the caller never sees the 401.
	if err := srv.Restart(); err != nil {
		t.Fatal(err)
	}
	if r, err := c.Get(ctx, "/redfish/v1/Systems/1"); err != nil || r.Status != 200 {
		t.Fatalf("after session expiry: %v %+v", err, r)
	}
	if c.Sessions() != 2 {
		t.Fatalf("sessions = %d, want a re-login", c.Sessions())
	}

	// Close logs the session out (a BMC has few session slots).
	if len(srv.Sessions()) != 1 {
		t.Fatalf("live sessions before Close = %v", srv.Sessions())
	}
	c.Close(ctx)
	if s := srv.Sessions(); len(s) != 0 {
		t.Fatalf("session left behind after Close: %v", s)
	}
}

func TestClientWrongPassword(t *testing.T) {
	t.Setenv("RF_TEST_PASSWORD", "wrong")
	srv := startMock(t, mockup.AuthSession)
	c, _ := NewClient(testSource(srv.URL()))
	_, err := c.Get(context.Background(), "/redfish/v1/Systems/1")
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Status != 401 || !strings.Contains(err.Error(), "password-env") {
		t.Fatalf("err = %v", err)
	}
	noSecret(t, err)
}

func TestClientPasswordUnset(t *testing.T) {
	srv := startMock(t, mockup.AuthSession)
	os.Unsetenv("RF_TEST_PASSWORD_UNSET")
	s := testSource(srv.URL())
	s.PasswordEnv = "RF_TEST_PASSWORD_UNSET"
	c, _ := NewClient(s)
	if _, err := c.Get(context.Background(), "/redfish/v1/Systems/1"); err == nil || !strings.Contains(err.Error(), "RF_TEST_PASSWORD_UNSET is not set") {
		t.Fatalf("err = %v", err)
	}
}

// A source with no account against a BMC that demands one: the source is
// down (an AuthError), not every tag Bad on a "connected" source.
func TestClientNoUserOnAuthBMC(t *testing.T) {
	srv := startMock(t, mockup.AuthSession)
	c, _ := NewClient(Source{ID: "X", Host: srv.URL()})
	var ae *AuthError
	if _, err := c.Get(context.Background(), "/redfish/v1/Systems/1"); !errors.As(err, &ae) || !strings.Contains(err.Error(), "no user") {
		t.Fatalf("err = %v", err)
	}
	// The service root stays readable without one (Redfish spec).
	if r, err := c.Get(context.Background(), "/redfish/v1"); err != nil || r.Status != 200 {
		t.Fatalf("root: %v %+v", err, r)
	}
}

// A BMC that answers the session POST with 405 does not do sessions: auto
// mode falls back to basic auth and stays there.
func TestClientBasicFallback(t *testing.T) {
	t.Setenv("RF_TEST_PASSWORD", testPassword)
	srv := startMock(t, mockup.AuthBasic)
	c, _ := NewClient(testSource(srv.URL()))
	for i := 0; i < 2; i++ {
		if r, err := c.Get(context.Background(), "/redfish/v1/Systems/1"); err != nil || r.Status != 200 {
			t.Fatalf("GET: %v %+v", err, r)
		}
	}
	if c.AuthMode() != AuthBasic || srv.Logins() != 0 {
		t.Fatalf("mode %s, logins %d", c.AuthMode(), srv.Logins())
	}
	// auth: session forbids the fallback.
	s := testSource(srv.URL())
	s.Auth = AuthSession
	c2, _ := NewClient(s)
	if _, err := c2.Get(context.Background(), "/redfish/v1/Systems/1"); err == nil || !strings.Contains(err.Error(), "forbids basic") {
		t.Fatalf("auth: session on a basic-only BMC: %v", err)
	}
}

// The wire shape of a login and a GET, byte for byte: what a BMC sees.
func TestClientWireShape(t *testing.T) {
	t.Setenv("RF_TEST_PASSWORD", testPassword)
	var mu sync.Mutex
	var seen []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" tok="+r.Header.Get("X-Auth-Token")+" odata="+r.Header.Get("OData-Version")+" "+string(body))
		mu.Unlock()
		if r.Method == http.MethodPost {
			// DMTF's Redfish-Mockup-Server answers a session POST with 204
			// and the token header — not 201. Any 2xx with a token is a
			// session.
			w.Header().Set("X-Auth-Token", "tok123")
			w.Header().Set("Location", "/redfish/v1/SessionService/Sessions/9")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	c, _ := NewClient(testSource(ts.URL))
	if _, err := c.Get(context.Background(), "/redfish/v1/Systems/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Post(context.Background(), "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{"ResetType": "ForceOff"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /redfish/v1/SessionService/Sessions tok= odata=4.0 {"Password":"` + testPassword + `","UserName":"admin"}`,
		`GET /redfish/v1/Systems/1 tok=tok123 odata=4.0 `,
		`POST /redfish/v1/Systems/1/Actions/ComputerSystem.Reset tok=tok123 odata=4.0 {"ResetType":"ForceOff"}`,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("wire:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
	if c.sessionURI != "/redfish/v1/SessionService/Sessions/9" {
		t.Fatalf("session uri = %q", c.sessionURI)
	}
}

// A self-signed BMC certificate fails verification by default, and the
// error names both fixes; insecure and ca-file each work.
func TestClientTLS(t *testing.T) {
	t.Setenv("RF_TEST_PASSWORD", testPassword)
	mock := mockup.New(smallTree(), mockup.Options{Auth: mockup.AuthSession, User: "admin", Password: testPassword})
	ts := httptest.NewTLSServer(mock)
	defer ts.Close()
	ctx := context.Background()

	c, _ := NewClient(testSource(ts.URL))
	_, err := c.Get(ctx, "/redfish/v1/Systems/1")
	var te *TLSError
	if !errors.As(err, &te) || !strings.Contains(err.Error(), "tls: {insecure: true}") || !strings.Contains(err.Error(), "ca-file") {
		t.Fatalf("self-signed: %v", err)
	}
	noSecret(t, err)

	s := testSource(ts.URL)
	s.TLS.Insecure = true
	c, _ = NewClient(s)
	if r, err := c.Get(ctx, "/redfish/v1/Systems/1"); err != nil || r.Status != 200 {
		t.Fatalf("insecure: %v %+v", err, r)
	}

	ca := filepath.Join(t.TempDir(), "bmc.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	s = testSource(ts.URL)
	s.TLS.CAFile = ca
	c, err = NewClient(s)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := c.Get(ctx, "/redfish/v1/Systems/1"); err != nil || r.Status != 200 {
		t.Fatalf("ca-file: %v %+v", err, r)
	}
	s.TLS.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := NewClient(s); err == nil || !strings.Contains(err.Error(), "ca-file") {
		t.Fatalf("missing ca-file: %v", err)
	}
}

// A refused connection is a transport error (not an AuthError), retried
// Retries times.
func TestClientTransportFailure(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	s := Source{ID: "X", Host: url, Retries: 2}
	c, _ := NewClient(s)
	_, err := c.Get(context.Background(), "/redfish/v1")
	var ae *AuthError
	if err == nil || errors.As(err, &ae) {
		t.Fatalf("refused: %v", err)
	}
}

// A transport failure retries a GET, never a POST: an action whose reply
// was lost may have run, and sent again a server restarts twice.
func TestClientRetriesReadsOnly(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.Method]++
		mu.Unlock()
		// No reply at all: the connection drops, a transport error.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()
	c, err := NewClient(Source{ID: "X", Host: srv.URL, Retries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), "/redfish/v1/Systems/1"); err == nil {
		t.Fatal("GET: want a transport error")
	}
	if _, err := c.Post(context.Background(), "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{"ResetType": "GracefulRestart"}); err == nil {
		t.Fatal("POST: want a transport error")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["GET"] != 3 || hits["POST"] != 1 {
		t.Fatalf("attempts = %v; want GET 3 (1 + 2 retries), POST 1", hits)
	}
}
