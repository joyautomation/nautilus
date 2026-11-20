// Package oidc signs browsers in at an OpenID Connect provider (#708 PR
// 6, docs/design/identity-audit.md §3): discovery from the issuer, the
// authorization-code flow with PKCE as a confidential client, and the ID
// token verified by go-oidc (signature, issuer, audience, expiry).
//
// It is the only package that imports go-oidc and x/oauth2
// (docs/design/context-map.md §2), and it imports no other nautilus
// package: naut run adapts a Client to server.OIDC. The server keeps the
// flow's state, checks the nonce, and decides; this package only talks to
// the provider and reads the token's claims.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Defaults for Config.
const (
	DefaultUsernameClaim = "preferred_username"
	DefaultGroupsClaim   = "groups"
)

// discoveryRetry is how long a failed discovery is answered from memory
// before the provider is asked again: a sign-in page reloaded in a loop
// must not hammer an unreachable provider.
var discoveryRetry = 15 * time.Second

// ErrUnreachable marks an error that is the provider out of reach
// (discovery, its keys, its token endpoint), not a refusal.
var ErrUnreachable = errors.New("the OIDC provider cannot be reached")

// Config is the auth.oidc block, with the client secret from the
// environment.
type Config struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	Name          string // the button's label; default from the issuer
	UsernameClaim string // default preferred_username
	GroupsClaim   string // default groups
	// HTTPClient talks to the provider; nil is a client with a 10 s
	// timeout.
	HTTPClient *http.Client
	// Now is the clock tokens are checked against; nil is time.Now.
	Now func() time.Time
}

// Client is one provider.
type Client struct {
	cfg  Config
	http *http.Client

	mu       sync.Mutex
	provider *gooidc.Provider
	failed   time.Time
	lastErr  error
}

// New checks cfg and returns its client. It does not reach the provider:
// discovery happens at the first sign-in, and again after a failure, so a
// controller starts while its provider is down (local users still sign
// in).
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Issuer)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("auth.oidc.issuer %q is not a URL like https://login.example.com/realm", cfg.Issuer)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname())) {
		return nil, fmt.Errorf("auth.oidc.issuer %q: use https (plain http only for a provider on this machine's loopback)", cfg.Issuer)
	}
	if cfg.ClientID == "" {
		return nil, errors.New("auth.oidc.client-id is empty: give the client id the provider registered for this controller")
	}
	if cfg.ClientSecret == "" {
		return nil, errors.New("auth.oidc: no client secret: set NAUTILUS_OIDC_SECRET to the secret the provider issued for this client")
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = DefaultUsernameClaim
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = DefaultGroupsClaim
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Provider is the provider's name for the sign-in button: Config.Name, a
// known provider's name by its issuer's host, or the host.
func (c *Client) Provider() string {
	if c.cfg.Name != "" {
		return c.cfg.Name
	}
	u, _ := url.Parse(c.cfg.Issuer)
	switch h := u.Hostname(); h {
	case "login.microsoftonline.com":
		return "Microsoft"
	case "accounts.google.com":
		return "Google"
	default:
		return h
	}
}

// Issuer is the configured issuer.
func (c *Client) Issuer() string { return c.cfg.Issuer }

func (c *Client) ctx(ctx context.Context) context.Context {
	return gooidc.ClientContext(ctx, c.http)
}

// discover is the provider's discovery document, fetched once and kept;
// a failure is kept for discoveryRetry.
func (c *Client) discover(ctx context.Context) (*gooidc.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != nil {
		return c.provider, nil
	}
	if c.lastErr != nil && time.Since(c.failed) < discoveryRetry {
		return nil, c.lastErr
	}
	p, err := gooidc.NewProvider(c.ctx(ctx), c.cfg.Issuer)
	if err != nil {
		c.failed = time.Now()
		c.lastErr = fmt.Errorf("%w: discovery at %s: %v", ErrUnreachable, c.cfg.Issuer, err)
		return nil, c.lastErr
	}
	c.provider, c.lastErr = p, nil
	return p, nil
}

// Check fetches the discovery document now, for naut run to say at the
// start whether the provider answers.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.discover(ctx)
	return err
}

func (c *Client) oauth(p *gooidc.Provider, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.cfg.ClientID,
		ClientSecret: c.cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirect,
		Scopes:       []string{gooidc.ScopeOpenID, "profile", "email"},
	}
}

// AuthCodeURL is the authorization endpoint for one sign-in: state, the
// nonce the ID token must carry, and the S256 challenge of verifier.
func (c *Client) AuthCodeURL(ctx context.Context, redirect, state, nonce, verifier string) (string, error) {
	p, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	return c.oauth(p, redirect).AuthCodeURL(state, gooidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// Identity is what a verified ID token says.
type Identity struct {
	Issuer, Subject string
	Name            string // the username claim's value, as given
	NameClaim       string
	Nonce           string
	Groups          []string
	GroupsOverage   bool
	AuthTime        time.Time
}

// Exchange redeems code (with verifier, at the redirect URI the sign-in
// started with) and verifies the ID token it brings. The nonce is the
// caller's to check: go-oidc's Verify does not.
func (c *Client) Exchange(ctx context.Context, redirect, code, verifier string) (Identity, error) {
	p, err := c.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	tok, err := c.oauth(p, redirect).Exchange(c.ctx(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			// The provider answered and refused: an expired or reused
			// code, a wrong secret, a verifier that does not match.
			return Identity{}, fmt.Errorf("the token endpoint refused the code: %s", tokenError(re))
		}
		return Identity{}, fmt.Errorf("%w: the token endpoint: %v", ErrUnreachable, err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, errors.New("the token endpoint's answer has no id_token (is the openid scope allowed for this client?)")
	}
	// p.Verifier keeps one key set for the provider (fetched once, again
	// on an unknown key id), with the HTTP client discovery used.
	v := p.Verifier(&gooidc.Config{ClientID: c.cfg.ClientID, Now: c.cfg.Now})
	idt, err := v.Verify(c.ctx(ctx), raw)
	if err != nil {
		return Identity{}, fmt.Errorf("the ID token does not verify: %v", err)
	}
	return c.identity(idt)
}

// tokenError is a token endpoint's refusal as a line for the journal: its
// error code and description, never the response body.
func tokenError(re *oauth2.RetrieveError) string {
	if re.ErrorCode == "" {
		return fmt.Sprintf("HTTP %d", re.Response.StatusCode)
	}
	if re.ErrorDescription != "" {
		d := re.ErrorDescription
		if len(d) > 200 {
			d = d[:200]
		}
		return re.ErrorCode + ": " + d
	}
	return re.ErrorCode
}

// claims are what Exchange reads beyond the registered claims go-oidc
// checks.
func (c *Client) identity(idt *gooidc.IDToken) (Identity, error) {
	var all map[string]any
	if err := idt.Claims(&all); err != nil {
		return Identity{}, fmt.Errorf("reading the ID token's claims: %v", err)
	}
	id := Identity{Issuer: idt.Issuer, Subject: idt.Subject, Nonce: idt.Nonce, NameClaim: c.cfg.UsernameClaim}
	if s, ok := all[c.cfg.UsernameClaim].(string); ok {
		id.Name = s
	}
	switch g := all[c.cfg.GroupsClaim].(type) {
	case nil:
	case []any:
		for _, e := range g {
			s, ok := e.(string)
			if !ok {
				return Identity{}, fmt.Errorf("the %s claim holds something other than strings", c.cfg.GroupsClaim)
			}
			id.Groups = append(id.Groups, s)
		}
	case string:
		// One group, as some providers send it.
		id.Groups = []string{g}
	default:
		return Identity{}, fmt.Errorf("the %s claim is not a list of group ids", c.cfg.GroupsClaim)
	}
	// Entra leaves the groups out past its limit and says so: a
	// _claim_names entry for the claim, or hasgroups.
	if names, ok := all["_claim_names"].(map[string]any); ok {
		if _, over := names[c.cfg.GroupsClaim]; over {
			id.GroupsOverage = true
		}
	}
	if b, ok := all["hasgroups"].(bool); ok && b {
		id.GroupsOverage = true
	}
	if at, ok := all["auth_time"].(float64); ok && at > 0 {
		id.AuthTime = time.Unix(int64(at), 0).UTC()
	}
	id.Name = strings.TrimSpace(id.Name)
	return id, nil
}
