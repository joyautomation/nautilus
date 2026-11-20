package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"time"

	"github.com/joyautomation/nautilus/auth/oidc"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/server"
)

// oidcSecretEnv names the variable auth.oidc's client secret comes from.
const oidcSecretEnv = "NAUTILUS_OIDC_SECRET"

// oidcSeam adapts auth/oidc's client to the server's seam (server.OIDC):
// the server links no OIDC module (docs/design/context-map.md §2).
type oidcSeam struct{ c *oidc.Client }

func (s oidcSeam) Provider() string { return s.c.Provider() }

func (s oidcSeam) AuthCodeURL(ctx context.Context, req server.OIDCAuthRequest) (string, error) {
	u, err := s.c.AuthCodeURL(ctx, req.RedirectURI, req.State, req.Nonce, req.Verifier)
	return u, seamErr(err)
}

func (s oidcSeam) Exchange(ctx context.Context, redirect, code, verifier string) (server.OIDCIdentity, error) {
	id, err := s.c.Exchange(ctx, redirect, code, verifier)
	if err != nil {
		return server.OIDCIdentity{}, seamErr(err)
	}
	return server.OIDCIdentity{
		Issuer: id.Issuer, Subject: id.Subject, Name: id.Name, NameClaim: id.NameClaim, Nonce: id.Nonce,
		Groups: id.Groups, GroupsOverage: id.GroupsOverage, AuthTime: id.AuthTime,
	}, nil
}

// seamErr carries "the provider is out of reach" across the seam.
func seamErr(err error) error {
	if errors.Is(err, oidc.ErrUnreachable) {
		return fmt.Errorf("%w: %v", server.ErrOIDCUnreachable, err)
	}
	return err
}

// withOIDC puts auth.oidc's provider on sopts (nil, nil without one), its
// client secret from NAUTILUS_OIDC_SECRET: one that is unset refuses to
// start, naming the variable.
func withOIDC(sopts *server.Options, proj *project.Project, getenv func(string) string) (*oidc.Client, error) {
	o := proj.AuthOIDC
	if o == nil {
		return nil, nil
	}
	c, err := oidc.New(oidc.Config{
		Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: getenv(oidcSecretEnv),
		Name: o.Name, UsernameClaim: o.UsernameClaim, GroupsClaim: o.GroupsClaim,
	})
	if err != nil {
		return nil, err
	}
	sopts.OIDC, sopts.OIDCRedirectURL = oidcSeam{c}, o.RedirectURL
	return c, nil
}

// sayOIDC tells, at the start, the redirect URI to register at the
// provider and whether the provider answers. The check runs in the
// background: a provider out of reach never holds the controller up, and
// local users still sign in.
func sayOIDC(ctx context.Context, c *oidc.Client, srv *server.Server, tls bool, listen string, w io.Writer) {
	if c == nil {
		return
	}
	_, port, _ := net.SplitHostPort(listen)
	uris := srv.OIDCRedirectURIs(tls, port)
	sort.Strings(uris)
	switch {
	case len(uris) == 0:
		scheme := "http"
		if tls {
			scheme = "https"
		}
		fmt.Fprintf(w, "auth: OIDC sign-in at %s: register the redirect URI %s://<the address you open the controller at>%s with the provider (set auth.oidc.redirect-url, or name the host in NAUTILUS_ALLOWED_HOSTS, to fix it)\n", c.Issuer(), scheme, project.OIDCCallbackPath)
	default:
		for _, u := range uris {
			fmt.Fprintf(w, "auth: OIDC sign-in at %s: register the redirect URI %s with the provider\n", c.Issuer(), u)
		}
	}
	go func() {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := c.Check(cctx); err != nil {
			fmt.Fprintf(w, "auth: WARNING: %v; OIDC sign-in retries at each attempt, and local users still sign in with a password\n", err)
		}
	}()
}
