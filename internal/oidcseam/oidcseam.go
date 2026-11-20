// Package oidcseam is the seam between the operator surface and an
// OpenID Connect provider (#708 PR 6): server keeps the sign-in flow, the
// sessions and every decision; auth/oidc talks to the provider; naut run
// adapts one to the other.
//
// It is internal on purpose: Options.OIDC's type cannot be named, nor
// implemented, from outside the module, so the seam is not stable Go API
// while #708 may still change its shape (elevation at the provider came in 6b).
// Revisit when #708 closes. It imports only the standard library.
package oidcseam

import (
	"context"
	"errors"
	"time"
)

// Provider is one OIDC provider, as the server uses it.
type Provider interface {
	// Provider is the provider's name for the sign-in page's button
	// ("Sign in with <Provider>").
	Provider() string
	// AuthCodeURL is the provider's authorization endpoint for one
	// sign-in: client id, redirect URI, scopes, state, nonce, and the
	// S256 challenge of the verifier. It may need the provider's
	// discovery document, so it can fail (ErrUnreachable).
	AuthCodeURL(ctx context.Context, req AuthRequest) (string, error)
	// Exchange redeems code at the token endpoint, with the verifier and
	// the redirect URI the sign-in started with, and verifies the ID
	// token: its signature against the provider's keys, its issuer, its
	// audience (the client id) and its expiry. It does not check the
	// nonce; the server does, against the one it sent.
	Exchange(ctx context.Context, redirectURI, code, verifier string) (Identity, error)
}

// AuthRequest is one sign-in's parameters for Provider.AuthCodeURL.
type AuthRequest struct {
	RedirectURI string
	State       string
	Nonce       string
	Verifier    string // the PKCE verifier; the URL carries its S256 challenge
	// Login asks the provider for the person's credentials again, whatever
	// session it holds for them (prompt=login, max_age=0): a re-
	// authentication for the elevated permissions (#708 PR 6b). OIDC Core
	// then requires auth_time in the ID token.
	Login bool
}

// Identity is what a verified ID token says.
type Identity struct {
	Issuer  string
	Subject string // sub: the account, stable at its issuer
	// Name is the username claim's value as given, and NameClaim the
	// claim it was read from (for a refusal that names it).
	Name      string
	NameClaim string
	Nonce     string
	// Groups are the groups claim's ids. GroupsOverage says the provider
	// left them out for having too many (Entra's "overage": a pointer to
	// the Graph API instead of the list).
	Groups        []string
	GroupsOverage bool
	// AuthTime is auth_time, when the person last entered credentials at
	// the provider; zero when the token has none. An OIDC session holds the
	// elevated permissions only within auth.elevation of it.
	AuthTime time.Time
}

// ErrUnreachable marks an error that is the provider being out of reach
// (discovery, keys or the token endpoint), not a refusal: the sign-in
// page says to try again, and that local users still can.
var ErrUnreachable = errors.New("the OIDC provider cannot be reached")
