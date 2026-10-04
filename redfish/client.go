// client.go is the wire layer: one net/http client per BMC, requests
// strictly sequential, a Redfish session (POST
// /redfish/v1/SessionService/Sessions → X-Auth-Token) re-created on 401,
// and basic auth when the service refuses sessions. No dependency beyond
// the standard library (brief §1).
//
// The error split is the driver's quality contract: Get returns an error
// only for a TRANSPORT failure — the BMC did not answer, TLS refused it,
// or it refused our credentials — which the poll loop counts toward the
// source's failure ladder. An HTTP status (404 on one resource, 503 while
// the BMC is busy, 403 on a resource the account may not read) comes back
// as a Response, and the driver marks exactly the tags bound to that
// resource Bad.
package redfish

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultTimeout bounds one request (brief §3.2).
const DefaultTimeout = 10 * time.Second

// SessionsPath is the standard session collection.
const SessionsPath = "/redfish/v1/SessionService/Sessions"

// maxBody caps one response: a BMC resource is kilobytes; a runaway body
// (a log dump behind a wrong path) must not eat the controller's memory.
const maxBody = 8 << 20

// Response is one answered request: its status and body.
type Response struct {
	Status int
	Body   []byte
}

// OK reports a 2xx status.
func (r Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Fetcher is the transport seam: the driver, the importer and `browse` all
// talk to a BMC through it, so unit tests substitute a fake with no socket
// (WithFetcher). *Client is the real one.
type Fetcher interface {
	// Get fetches one resource. err is a transport failure only.
	Get(ctx context.Context, path string) (Response, error)
	// Post sends a JSON body (an action, a session).
	Post(ctx context.Context, path string, body any) (Response, error)
	// Close ends the session (best effort) and releases connections.
	Close(ctx context.Context)
}

// AuthError is a credential refusal: 401 after a fresh login, or the login
// itself refused. A transport failure for the poll loop — retrying with the
// same password every interval would lock some BMC accounts out, so the
// backoff ladder is the right pace — but a different message from a
// timeout.
type AuthError struct {
	Status int
	Detail string
}

func (e *AuthError) Error() string {
	msg := fmt.Sprintf("authentication refused (HTTP %d)", e.Status)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg + " — check the user and the password its password-env/password-file names"
}

// TLSError wraps a certificate the client would not trust, naming the fix.
type TLSError struct{ Err error }

func (e *TLSError) Error() string {
	return fmt.Sprintf("TLS: %v — BMCs ship self-signed certificates: set `tls: {ca-file: /path/to/bmc-ca.pem}` on the source to trust it, or `tls: {insecure: true}` to skip verification", e.Err)
}
func (e *TLSError) Unwrap() error { return e.Err }

// Client is one BMC's HTTP client.
type Client struct {
	base    *url.URL
	hc      *http.Client
	user    string
	passFn  func() (string, error)
	mode    string // auto | session | basic | none
	retries int

	mu         sync.Mutex // serialises requests: one BMC, one conversation
	token      string
	sessionURI string
	basic      bool   // auto fell back to basic auth
	password   string // cached; dropped on a 401
	havePass   bool

	sessions atomic.Uint64 // sessions created, for tests and status
}

// NewClient builds the client for one source. It does not dial.
func NewClient(s Source) (*Client, error) {
	base, err := s.BaseURL()
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		MaxConnsPerHost:     1, // one connection per BMC (brief §3.2)
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false, // BMC web servers are HTTP/1.1 and some choke on h2
	}
	if base.Scheme == "https" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		switch {
		case s.TLS.Insecure:
			cfg.InsecureSkipVerify = true //nolint:gosec // the operator's explicit, per-source choice
		case s.TLS.CAFile != "":
			pem, err := os.ReadFile(s.TLS.CAFile)
			if err != nil {
				return nil, fmt.Errorf("tls ca-file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("tls ca-file %s: no PEM certificates in it", s.TLS.CAFile)
			}
			cfg.RootCAs = pool
		}
		tr.TLSClientConfig = cfg
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	mode := s.Auth
	if mode == "" {
		mode = AuthAuto
	}
	if s.User == "" {
		mode = AuthNone
	}
	c := &Client{
		base:    base,
		hc:      &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: noRedirect},
		user:    s.User,
		passFn:  s.password,
		mode:    mode,
		retries: s.Retries,
	}
	return c, nil
}

// noRedirect: a BMC that redirects /redfish/v1 elsewhere is misconfigured,
// and following it would carry the credentials to wherever it points.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Sessions reports how many sessions this client has created.
func (c *Client) Sessions() uint64 { return c.sessions.Load() }

// AuthMode reports what the client is actually using: session, basic or
// none — "auto" resolves on the first request.
func (c *Client) AuthMode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.mode == AuthNone:
		return AuthNone
	case c.mode == AuthBasic || c.basic:
		return AuthBasic
	case c.token != "":
		return AuthSession
	}
	return c.mode
}

// Get fetches path (an absolute /redfish/v1 URI).
func (c *Client) Get(ctx context.Context, path string) (Response, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

// Post sends body as JSON.
func (c *Client) Post(ctx context.Context, path string, body any) (Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	return c.do(ctx, http.MethodPost, path, raw)
}

// Close logs the session out (best effort — a BMC holds only a few
// sessions, and one leaked per restart runs it out) and drops idle
// connections.
func (c *Client) Close(ctx context.Context) {
	c.mu.Lock()
	uri, tok := c.sessionURI, c.token
	c.token, c.sessionURI = "", ""
	c.mu.Unlock()
	if tok != "" && uri != "" {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url(uri), nil)
		if err == nil {
			req.Header.Set("X-Auth-Token", tok)
			if resp, err := c.hc.Do(req); err == nil {
				drain(resp)
			}
		}
		cancel()
	}
	c.hc.CloseIdleConnections()
}

func (c *Client) url(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	u := *c.base
	u.Path = path
	return u.String()
}

// do runs one request with the auth dance: ensure credentials, send, and on
// a 401 with a session re-login once and resend.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; ; attempt++ {
		if err := c.ensureAuthLocked(ctx); err != nil {
			return Response{}, err
		}
		resp, err := c.sendLocked(ctx, method, path, body)
		if err != nil {
			// Only a read is retried. A POST that timed out may well have
			// run (a reset, a power action): sent again, a server restarts
			// twice. A 401 below is different: the BMC refused it unrun.
			idempotent := method == http.MethodGet || method == http.MethodHead
			if idempotent && attempt < c.retries && ctx.Err() == nil && !isTLS(err) {
				continue
			}
			return Response{}, err
		}
		if resp.Status != http.StatusUnauthorized {
			return resp, nil
		}
		if c.mode == AuthNone {
			// Not a per-resource refusal: nothing on this BMC is readable
			// without an account, so the source is down, not its tags Bad.
			return Response{}, &AuthError{Status: resp.Status, Detail: "the BMC requires credentials and the source has no user"}
		}
		// 401. A session may simply have expired (BMC timeout, BMC
		// reboot): log in again once. Anything else is the password.
		if c.token != "" && attempt == 0 {
			c.token, c.sessionURI = "", ""
			c.havePass = false // re-read: a rotated k8s Secret lands here
			continue
		}
		c.token, c.sessionURI = "", ""
		c.havePass = false
		return Response{}, &AuthError{Status: resp.Status, Detail: redfishMessage(resp.Body)}
	}
}

func (c *Client) sendLocked(ctx context.Context, method, path string, body []byte) (Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rd)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OData-Version", "4.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case c.token != "":
		req.Header.Set("X-Auth-Token", c.token)
	case c.mode == AuthBasic || c.basic:
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.password)))
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return Response{}, classify(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Response{}, classify(err)
	}
	return Response{Status: resp.StatusCode, Body: b}, nil
}

// ensureAuthLocked makes sure the next request carries credentials: reads
// the password, and in auto/session mode logs in.
func (c *Client) ensureAuthLocked(ctx context.Context) error {
	if c.mode == AuthNone {
		return nil
	}
	if !c.havePass {
		p, err := c.passFn()
		if err != nil {
			return err
		}
		c.password, c.havePass = p, true
	}
	if c.mode == AuthBasic || c.basic || c.token != "" {
		return nil
	}
	return c.loginLocked(ctx)
}

// loginLocked creates a session. The DMTF mockup server answers 204 and a
// real BMC 201; either way the token is the header, so any 2xx with an
// X-Auth-Token is a session. A service that answers the POST without one,
// or with 404/405/501, does not do sessions: auto mode falls back to basic.
func (c *Client) loginLocked(ctx context.Context) error {
	raw, _ := json.Marshal(map[string]string{"UserName": c.user, "Password": c.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(SessionsPath), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OData-Version", "4.0")
	resp, err := c.hc.Do(req)
	if err != nil {
		return classify(err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	resp.Body.Close()
	tok := resp.Header.Get("X-Auth-Token")
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300 && tok != "":
		c.token = tok
		c.sessionURI = sessionLocation(resp, body)
		c.sessions.Add(1)
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		c.havePass = false
		return &AuthError{Status: resp.StatusCode, Detail: redfishMessage(body)}
	case c.mode == AuthSession:
		return fmt.Errorf("session login refused (HTTP %d, no X-Auth-Token) and auth: session forbids basic auth", resp.StatusCode)
	}
	c.basic = true
	return nil
}

// sessionLocation: the Location header, else the body's @odata.id.
func sessionLocation(resp *http.Response, body []byte) string {
	if loc := resp.Header.Get("Location"); loc != "" {
		if u, err := url.Parse(loc); err == nil && u.Path != "" {
			return u.Path
		}
	}
	var doc struct {
		ID string `json:"@odata.id"`
	}
	if json.Unmarshal(body, &doc) == nil {
		return doc.ID
	}
	return ""
}

// redfishMessage pulls the human line out of a Redfish error body
// ({"error": {"message": …, "@Message.ExtendedInfo": [{"Message": …}]}}).
func redfishMessage(body []byte) string {
	var doc struct {
		Error struct {
			Message string `json:"message"`
			Ext     []struct {
				Message string `json:"Message"`
			} `json:"@Message.ExtendedInfo"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	if len(doc.Error.Ext) > 0 && doc.Error.Ext[0].Message != "" {
		return doc.Error.Ext[0].Message
	}
	return doc.Error.Message
}

// classify turns a certificate failure into the TLSError that names the
// fix; everything else passes through.
func classify(err error) error {
	var cve *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var he x509.HostnameError
	var ci x509.CertificateInvalidError
	if errors.As(err, &cve) || errors.As(err, &ua) || errors.As(err, &he) || errors.As(err, &ci) {
		return &TLSError{Err: stripURL(err)}
	}
	return err
}

// stripURL drops net/http's `Get "https://…":` prefix for the TLS message;
// the source row already says which BMC.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func isTLS(err error) bool {
	var te *TLSError
	return errors.As(err, &te)
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}
