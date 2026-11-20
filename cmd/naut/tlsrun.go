package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/server"
)

// tlsSpec is a project's server.tls, as tlsSource takes it.
type tlsSpec struct {
	cert, key string
	// selfSigned is server.tls: self-signed, made in the state directory
	// stateDir (auth.state-dir), else $STATE_DIRECTORY, else (devFallback,
	// a checkout) $XDG_STATE_HOME/nautilus/<name>.
	selfSigned bool
	stateDir   string
	name       string
	// devFallback allows the $XDG_STATE_HOME state directory; listen is
	// the address served, a name the certificate is made for.
	devFallback bool
	listen      string
	// say gets what is to be told about a self-signed certificate.
	say func(string)
	// renew: this command renews the self-signed certificate near its
	// expiry. Only naut run does; the others make one when there is none
	// and serve it as it is, so no other process swaps the certificate a
	// running controller serves.
	renew bool
}

// selfSignedDir is the state directory and name of spec's self-signed
// certificate; use is false when it serves none (no self-signed, or
// NAUTILUS_TLS_CERT/_KEY set, which win).
func (spec tlsSpec) selfSignedDir(projectDir string, getenv func(string) string) (dir, name string, use bool, err error) {
	if !spec.selfSigned || getenv("NAUTILUS_TLS_CERT") != "" || getenv("NAUTILUS_TLS_KEY") != "" {
		return "", "", false, nil
	}
	name = spec.name
	if name == "" {
		name = "nautilus"
	}
	dir, err = controllerStateDir("the self-signed certificate (server.tls: self-signed)", spec.stateDir, name, projectDir, spec.devFallback, getenv)
	if err != nil {
		return "", "", true, fmt.Errorf("tls: %w", err)
	}
	return dir, name, true, nil
}

// tlsSpecOf is a manifest's server.tls (nil: none) as a tlsSpec.
func tlsSpecOf(t *project.TLSConfig, stateDir, name, listen string, devFallback bool, say func(string)) tlsSpec {
	s := tlsSpec{stateDir: stateDir, name: name, listen: listen, devFallback: devFallback, say: say}
	if t != nil {
		s.cert, s.key, s.selfSigned = t.Cert, t.Key, t.SelfSigned
	}
	return s
}

// tlsSource loads the tag API's certificate (#708, server.tls): the
// manifest's cert and key, or NAUTILUS_TLS_CERT and NAUTILUS_TLS_KEY when
// set (they win over self-signed too), or the self-signed one in the
// state directory. Nil when none is named. A path inside the project is
// refused: a private key never belongs in a checkout, a build or git.
func tlsSource(spec tlsSpec, projectDir string, getenv func(string) string) (*server.CertSource, error) {
	cert, key := spec.cert, spec.key
	envCert, envKey := getenv("NAUTILUS_TLS_CERT"), getenv("NAUTILUS_TLS_KEY")
	if envCert != "" {
		cert = envCert
	}
	if envKey != "" {
		key = envKey
	}
	sd, name, use, err := spec.selfSignedDir(projectDir, getenv)
	if err != nil {
		return nil, err
	}
	if use {
		c, err := ensureSelfSigned(sd, name, selfSignedHosts(spec.listen), time.Now(), spec.renew)
		if err != nil {
			return nil, err
		}
		if spec.say != nil {
			spec.say(selfSignedNotice(c))
		}
		return server.NewCertSource(c.Path, c.Path)
	}
	if cert == "" && key == "" {
		return nil, nil
	}
	if cert == "" || key == "" {
		return nil, errors.New("tls: a certificate needs its key and a key its certificate (server.tls, or NAUTILUS_TLS_CERT and NAUTILUS_TLS_KEY)")
	}
	for _, p := range []string{cert, key} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("tls: %s is relative: give an absolute path on the box, outside the project", p)
		}
		if projectDir == "" {
			continue
		}
		proj, err := filepath.Abs(projectDir)
		if err != nil {
			return nil, err
		}
		if rel, err := filepath.Rel(proj, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("tls: %s is inside the project %s: a private key never belongs in a checkout or git; keep it on the box, outside it", p, proj)
		}
	}
	return server.NewCertSource(cert, key)
}

// selfSignedCheckEvery is how often a running naut run looks at its
// self-signed certificate's expiry.
var selfSignedCheckEvery = 24 * time.Hour

// keepSelfSignedFresh is naut run's daily look at its self-signed
// certificate: from selfSignedRenew before its expiry it makes a new one,
// which the tag API loads within a minute (and journals, tls.changed). A
// controller that runs for a year never serves an expired one. Returns at
// once when spec serves no self-signed certificate.
func keepSelfSignedFresh(ctx context.Context, spec tlsSpec, projectDir string, getenv func(string) string, now func() time.Time, say func(string)) {
	sd, name, use, err := spec.selfSignedDir(projectDir, getenv)
	if err != nil || !use {
		return
	}
	t := time.NewTicker(selfSignedCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, err := ensureSelfSigned(sd, name, selfSignedHosts(spec.listen), now(), true)
		switch {
		case err != nil:
			say(fmt.Sprintf("WARNING: TLS: the self-signed certificate could not be checked or renewed (%v); the one loaded keeps being served\n", err))
		case c.Renewed:
			say("RENEWED " + selfSignedNotice(c))
		}
	}
}

// facadeListener is how naut logix serve and naut siemens serve listen:
// over TLS when src is set, and returns the scheme to show. Without TLS on
// an address beyond this machine it warns that what clients send (their
// tokens, the values they write) crosses the network in cleartext: these
// are local tools, so it warns rather than refuses.
func facadeListener(ln net.Listener, src *server.CertSource, listen, cmd string, stderr io.Writer) (net.Listener, string) {
	if src != nil {
		return tls.NewListener(ln, src.Config()), "https"
	}
	if !loopbackListen(listen) {
		fmt.Fprintf(stderr, "%s: WARNING: --listen %s reaches beyond this machine over plain HTTP: tokens, and every value written, cross the network in CLEARTEXT. Give the project a server.tls (or set NAUTILUS_TLS_CERT and NAUTILUS_TLS_KEY), or listen on localhost.\n", cmd, listen)
	}
	return ln, "http"
}

// The tag API's connection limits (naut run, naut logix serve, naut
// siemens serve): a client gets apiReadHeaderTimeout to finish its TLS
// handshake and send a request's headers, so one that stalls (or opens
// connections and sends nothing) cannot hold them forever; a kept-alive
// connection idle for apiIdleTimeout is closed. There is no write timeout:
// the live stream (/api/stream) stays open as long as its client reads.
var (
	apiReadHeaderTimeout = 10 * time.Second
	apiIdleTimeout       = 2 * time.Minute
)

// apiHTTPServer is the tag API's http.Server, with those limits.
func apiHTTPServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: apiReadHeaderTimeout, IdleTimeout: apiIdleTimeout}
}
