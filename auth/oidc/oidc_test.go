package oidc

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth/oidc/oidctest"
)

const redirect = "https://plc.example/auth/oidc/callback"

func client(t *testing.T, idp *oidctest.IdP) *Client {
	t.Helper()
	c, err := New(Config{Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.Secret})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// signIn runs one sign-in at idp as a person whose ID token holds
// claims, and returns what Exchange makes of it.
func signIn(t *testing.T, c *Client, idp *oidctest.IdP, claims map[string]any) (Identity, error) {
	t.Helper()
	ctx := context.Background()
	verifier := strings.Repeat("v", 43)
	to, err := c.AuthCodeURL(ctx, redirect, "state-1", "nonce-1", verifier, false)
	if err != nil {
		t.Fatal(err)
	}
	back, _ := url.Parse(idp.Authorize(t, to, claims))
	return c.Exchange(ctx, redirect, back.Query().Get("code"), verifier)
}

func TestExchangeReadsAVerifiedToken(t *testing.T) {
	idp := oidctest.New(t, "naut", "s3cret")
	c := client(t, idp)
	id, err := signIn(t, c, idp, map[string]any{"preferred_username": " JSmith@corp.example ", "groups": []string{"g-1", "g-2"}, "auth_time": 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	if id.Issuer != idp.URL || id.Subject != "sub-1" || id.Name != "JSmith@corp.example" || id.Nonce != "nonce-1" {
		t.Errorf("identity %+v", id)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "g-1" || id.GroupsOverage {
		t.Errorf("groups %v overage %v", id.Groups, id.GroupsOverage)
	}
	if !id.AuthTime.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("auth_time %v", id.AuthTime)
	}
}

func TestExchangeRefusesWhatDoesNotVerify(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims map[string]any
		bad    bool
		want   string
	}{
		{"signed by another key", nil, true, "does not verify"},
		{"another audience", map[string]any{"aud": "someone-else"}, false, "does not verify"},
		{"expired", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}, false, "does not verify"},
		{"another issuer", map[string]any{"iss": "https://evil.example"}, false, "does not verify"},
		{"no subject", map[string]any{"sub": nil}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idp := oidctest.New(t, "naut", "s3cret")
			idp.BadKey = tc.bad
			id, err := signIn(t, client(t, idp), idp, tc.claims)
			if tc.want == "" {
				// go-oidc lets a token without sub through: the server
				// refuses an identity without one.
				if err == nil && id.Subject != "" {
					t.Fatalf("no sub, yet subject %q", id.Subject)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, ErrUnreachable) {
				t.Fatalf("err = %v, want a refusal containing %q", err, tc.want)
			}
		})
	}
}

func TestExchangeRefusesAWrongVerifierOrSecret(t *testing.T) {
	idp := oidctest.New(t, "naut", "s3cret")
	c := client(t, idp)
	ctx := context.Background()
	to, err := c.AuthCodeURL(ctx, redirect, "s", "n", strings.Repeat("v", 43), false)
	if err != nil {
		t.Fatal(err)
	}
	back, _ := url.Parse(idp.Authorize(t, to, nil))
	_, err = c.Exchange(ctx, redirect, back.Query().Get("code"), strings.Repeat("w", 43))
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") || errors.Is(err, ErrUnreachable) {
		t.Fatalf("wrong verifier: %v", err)
	}

	wrong, _ := New(Config{Issuer: idp.URL, ClientID: "naut", ClientSecret: "nope"})
	to, _ = wrong.AuthCodeURL(ctx, redirect, "s", "n", strings.Repeat("v", 43), false)
	back, _ = url.Parse(idp.Authorize(t, to, nil))
	if _, err := wrong.Exchange(ctx, redirect, back.Query().Get("code"), strings.Repeat("v", 43)); err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("wrong secret: %v", err)
	}
}

func TestAuthCodeURLCarriesPKCEAndNonce(t *testing.T) {
	idp := oidctest.New(t, "naut", "s3cret")
	to, err := client(t, idp).AuthCodeURL(context.Background(), redirect, "st", "no", strings.Repeat("v", 43), false)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(to)
	q := u.Query()
	if q.Get("state") != "st" || q.Get("nonce") != "no" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("redirect_uri") != redirect {
		t.Errorf("auth URL %s", to)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("scope %q", q.Get("scope"))
	}
}

func TestGroupsOverageAndShapes(t *testing.T) {
	idp := oidctest.New(t, "naut", "s3cret")
	c := client(t, idp)
	id, err := signIn(t, c, idp, map[string]any{
		// Entra's overage shape: the groups are at a Graph endpoint.
		"_claim_names":   map[string]any{"groups": "src1"},
		"_claim_sources": map[string]any{"src1": map[string]any{"endpoint": "https://graph.example/v1.0/users/x/getMemberObjects"}},
	})
	if err != nil || !id.GroupsOverage {
		t.Errorf("_claim_names overage: %+v %v", id, err)
	}
	id, err = signIn(t, c, idp, map[string]any{"hasgroups": true})
	if err != nil || !id.GroupsOverage {
		t.Errorf("hasgroups overage: %+v %v", id, err)
	}
	id, err = signIn(t, c, idp, map[string]any{"groups": "only-one"})
	if err != nil || len(id.Groups) != 1 || id.Groups[0] != "only-one" {
		t.Errorf("a single group: %+v %v", id, err)
	}
	if _, err = signIn(t, c, idp, map[string]any{"groups": []any{"a", 7}}); err == nil {
		t.Error("a groups claim holding a number was taken")
	}
}

func TestDiscoveryDownIsUnreachableAndRetried(t *testing.T) {
	old := discoveryRetry
	discoveryRetry = time.Hour
	defer func() { discoveryRetry = old }()
	idp := oidctest.New(t, "naut", "s3cret")
	c := client(t, idp)
	idp.Close()
	_, err := c.AuthCodeURL(context.Background(), redirect, "s", "n", strings.Repeat("v", 43), false)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	// Answered from memory, without asking the provider again.
	c.mu.Lock()
	failed := c.failed
	c.mu.Unlock()
	if err2 := c.Check(context.Background()); !errors.Is(err2, ErrUnreachable) {
		t.Fatal(err2)
	}
	c.mu.Lock()
	again := c.failed
	c.mu.Unlock()
	if !again.Equal(failed) {
		t.Error("a failed discovery was retried within discoveryRetry")
	}
}

func TestNewRefusesABadConfig(t *testing.T) {
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{Issuer: "http://login.example", ClientID: "a", ClientSecret: "b"}, "use https"},
		{Config{Issuer: "not a url", ClientID: "a", ClientSecret: "b"}, "not a URL"},
		{Config{Issuer: "https://login.example", ClientSecret: "b"}, "client-id"},
		{Config{Issuer: "https://login.example", ClientID: "a"}, "NAUTILUS_OIDC_SECRET"},
	} {
		if _, err := New(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.cfg, err, tc.want)
		}
	}
	if _, err := New(Config{Issuer: "http://127.0.0.1:9", ClientID: "a", ClientSecret: "b"}); err != nil {
		t.Errorf("a loopback http issuer: %v", err)
	}
}

func TestProviderName(t *testing.T) {
	for issuer, want := range map[string]string{
		"https://login.microsoftonline.com/tenant/v2.0": "Microsoft",
		"https://accounts.google.com":                   "Google",
		"https://sso.plant.example/realms/ops":          "sso.plant.example",
	} {
		c, _ := New(Config{Issuer: issuer, ClientID: "a", ClientSecret: "b"})
		if got := c.Provider(); got != want {
			t.Errorf("%s: %q, want %q", issuer, got, want)
		}
	}
	c, _ := New(Config{Issuer: "https://x.example", ClientID: "a", ClientSecret: "b", Name: "Plant SSO"})
	if c.Provider() != "Plant SSO" {
		t.Error(c.Provider())
	}
}

// A re-authentication asks the provider for the credentials again
// (prompt=login, max_age=0); a sign-in leaves the provider's session be.
func TestAuthCodeURLAsksForLoginOnlyWhenTold(t *testing.T) {
	idp := oidctest.New(t, "naut", "s3cret")
	c := client(t, idp)
	for _, login := range []bool{false, true} {
		to, err := c.AuthCodeURL(context.Background(), redirect, "s", "n", strings.Repeat("v", 43), login)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(to)
		q := u.Query()
		if login && (q.Get("prompt") != "login" || q.Get("max_age") != "0") {
			t.Errorf("login: %s lacks prompt=login&max_age=0", to)
		}
		if !login && (q.Has("prompt") || q.Has("max_age")) {
			t.Errorf("sign-in: %s asks for a login", to)
		}
	}
}
