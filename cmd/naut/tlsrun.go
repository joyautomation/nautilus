package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/server"
)

// tlsSource loads the tag API's certificate (#708, server.tls): the
// manifest's cert and key, or NAUTILUS_TLS_CERT and NAUTILUS_TLS_KEY when
// set. Nil when neither names one. A path inside the project is refused:
// a private key never belongs in a checkout, a build or git.
func tlsSource(cert, key, projectDir string, getenv func(string) string) (*server.CertSource, error) {
	if c := getenv("NAUTILUS_TLS_CERT"); c != "" {
		cert = c
	}
	if k := getenv("NAUTILUS_TLS_KEY"); k != "" {
		key = k
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
