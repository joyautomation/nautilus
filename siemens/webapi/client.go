// Package webapi is a Go client for the PLC Web API of Siemens S7-1500,
// ET 200SP and S7-1200 G2 CPUs: JSON-RPC 2.0 over HTTPS at /api/jsonrpc,
// built into the CPU's web server and free of licences. It is the online
// plane of the Siemens target (docs/design/siemens-ide.md §4): symbolic
// browse, read and write, including optimized DBs, and the operating mode.
//
// Facts this client is built on, and where they come from:
//
//   - The method and parameter names (Api.Login {user, password},
//     PlcProgram.Browse {var, mode}, PlcProgram.Read/Write {var, value,
//     mode}, Plc.ReadOperatingMode) and the error codes in errors.go are
//     those of Siemens' own .NET client, github.com/siemens/simatic-s7-webserver-api.
//   - The token rides in the X-Auth-Token header and expires after about
//     120 s without an authenticated call (Web Server function manual).
//   - A request body may be at most 64 kB (FW < 3.1) or 128 kB (FW ≥ 3.1).
//     MaxBody defaults to the smaller, and ReadMany and WriteMany split
//     their batches to stay under it.
//
// Unverified until the bench CPU's web server is on (S2b): that an expired
// token answers PermissionDenied (this client logs in again once and
// retries), and the exact JSON shape of values that are not elementary.
package webapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultMaxBody is the request body limit of FW < 3.1; FW ≥ 3.1 allows
// 128 kB. The client never sends more.
const DefaultMaxBody = 64 * 1024

// DefaultMaxBatch bounds the number of calls in one bulk request, so a
// batch of tiny reads does not produce a reply the CPU cannot build.
const DefaultMaxBatch = 200

// Options configure a Client.
type Options struct {
	User     string
	Password string
	// CertSHA256 pins the CPU's certificate by the SHA-256 of its DER
	// bytes (hex, colons allowed). CPUs ship self-signed certificates, so
	// pinning is the normal way to trust one.
	CertSHA256 string
	// RootCAs trusts certificates issued by these CAs instead (a CPU with
	// a certificate from a plant CA).
	RootCAs *x509.CertPool
	// Timeout bounds each HTTP request. Default 10 s.
	Timeout  time.Duration
	MaxBody  int // default DefaultMaxBody
	MaxBatch int // default DefaultMaxBatch
}

// Client talks to one CPU. It is safe for concurrent use.
type Client struct {
	url    string
	opt    Options
	http   *http.Client
	mu     sync.Mutex
	token  string
	nextID int
}

// New builds a client for the CPU at host ("192.0.2.10" or "host:443").
// A host that is already a URL ("https://…") is used as the base, which is
// how tests reach a fake server.
func New(host string, opt Options) (*Client, error) {
	base := host
	if !strings.Contains(host, "://") {
		base = "https://" + host
	}
	if opt.Timeout == 0 {
		opt.Timeout = 10 * time.Second
	}
	if opt.MaxBody == 0 {
		opt.MaxBody = DefaultMaxBody
	}
	if opt.MaxBatch == 0 {
		opt.MaxBatch = DefaultMaxBatch
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opt.RootCAs}
	if opt.CertSHA256 != "" {
		want, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(opt.CertSHA256), ":", ""))
		if err != nil || len(want) != sha256.Size {
			return nil, fmt.Errorf("webapi: CertSHA256 %q is not a SHA-256 in hex", opt.CertSHA256)
		}
		// Pinning replaces chain verification: the pin is the trust.
		tlsCfg.InsecureSkipVerify = true
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("webapi: the CPU sent no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !bytes.Equal(got[:], want) {
				return fmt.Errorf("webapi: the CPU's certificate SHA-256 %x does not match the pinned %x", got, want)
			}
			return nil
		}
	}
	return &Client{
		url: strings.TrimRight(base, "/") + "/api/jsonrpc",
		opt: opt,
		http: &http.Client{
			Timeout:   opt.Timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg, MaxIdleConnsPerHost: 2},
		},
	}, nil
}

// CertificateSHA256 connects to host and returns the SHA-256 of the
// certificate it presents, for a person to compare and pin. It trusts
// nothing and sends nothing beyond the TLS handshake.
func CertificateSHA256(ctx context.Context, host string) (string, error) {
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", fmt.Errorf("webapi: %w", err)
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("webapi: no certificate")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// --- JSON-RPC -----------------------------------------------------------------

type request struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	ID      int    `json:"id"`
}

type response struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Call is one method call in a batch.
type Call struct {
	Method string
	Params any
}

// Reply is the outcome of one Call. Exactly one of Result and Err is set.
type Reply struct {
	Result json.RawMessage
	Err    error
}

func (c *Client) post(ctx context.Context, body []byte, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Auth-Token", token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("webapi: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("webapi: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("webapi: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return raw, resp.StatusCode, nil
}

// batch sends calls as one bulk request (or a plain request for one call)
// with the given token, and returns one Reply per call, in order.
func (c *Client) batch(ctx context.Context, calls []Call, token string) ([]Reply, error) {
	c.mu.Lock()
	base := c.nextID
	c.nextID += len(calls)
	c.mu.Unlock()
	reqs := make([]request, len(calls))
	for i, cl := range calls {
		reqs[i] = request{JSONRPC: "2.0", Method: cl.Method, Params: cl.Params, ID: base + i + 1}
	}
	var body []byte
	var err error
	if len(reqs) == 1 {
		body, err = json.Marshal(reqs[0])
	} else {
		body, err = json.Marshal(reqs)
	}
	if err != nil {
		return nil, fmt.Errorf("webapi: encoding request: %w", err)
	}
	if len(body) > c.opt.MaxBody {
		return nil, fmt.Errorf("webapi: request of %d bytes exceeds the %d-byte limit", len(body), c.opt.MaxBody)
	}
	raw, _, err := c.post(ctx, body, token)
	if err != nil {
		return nil, err
	}
	var resps []response
	if len(reqs) == 1 {
		var r response
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("webapi: bad reply: %v: %s", err, truncate(string(raw), 200))
		}
		resps = []response{r}
	} else if err := json.Unmarshal(raw, &resps); err != nil {
		return nil, fmt.Errorf("webapi: bad bulk reply: %v: %s", err, truncate(string(raw), 200))
	}
	byID := make(map[int]response, len(resps))
	for _, r := range resps {
		byID[r.ID] = r
	}
	out := make([]Reply, len(calls))
	for i, rq := range reqs {
		r, ok := byID[rq.ID]
		switch {
		case !ok:
			out[i].Err = fmt.Errorf("webapi: %s: no reply in the bulk response", rq.Method)
		case r.Error != nil:
			out[i].Err = &Error{Code: r.Error.Code, Message: r.Error.Message, Method: rq.Method}
		default:
			out[i].Result = r.Result
		}
	}
	return out, nil
}

// Do sends calls authenticated, logging in first if needed. When every
// reply says PermissionDenied and the client had a token, the token is
// assumed expired: it logs in again once and repeats the calls.
func (c *Client) Do(ctx context.Context, calls ...Call) ([]Reply, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	out, err := c.batch(ctx, calls, token)
	if err != nil || !allDenied(out) {
		return out, err
	}
	c.dropToken(token)
	if token, err = c.ensureToken(ctx); err != nil {
		return nil, err
	}
	return c.batch(ctx, calls, token)
}

func allDenied(rs []Reply) bool {
	for _, r := range rs {
		if Code(r.Err) != CodePermissionDenied {
			return false
		}
	}
	return len(rs) > 0
}

// call runs one authenticated call and decodes its result into out.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	rs, err := c.Do(ctx, Call{Method: method, Params: params})
	if err != nil {
		return err
	}
	return decode(rs[0], out)
}

// anon runs one call without a token (Api.Ping, Api.Version, …).
func (c *Client) anon(ctx context.Context, method string, out any) error {
	rs, err := c.batch(ctx, []Call{{Method: method}}, "")
	if err != nil {
		return err
	}
	return decode(rs[0], out)
}

func decode(r Reply, out any) error {
	if r.Err != nil {
		return r.Err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		return fmt.Errorf("webapi: decoding result %s: %w", truncate(string(r.Result), 100), err)
	}
	return nil
}

// --- session -----------------------------------------------------------------

func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok != "" {
		return tok, nil
	}
	return c.login(ctx)
}

func (c *Client) dropToken(tok string) {
	c.mu.Lock()
	if c.token == tok {
		c.token = ""
	}
	c.mu.Unlock()
}

// Login authenticates with the configured user. Calls log in on their own;
// Login is for checking credentials up front.
func (c *Client) Login(ctx context.Context) error {
	_, err := c.login(ctx)
	return err
}

func (c *Client) login(ctx context.Context) (string, error) {
	if c.opt.User == "" {
		return "", errors.New("webapi: no user configured (anonymous access is for commissioning only)")
	}
	rs, err := c.batch(ctx, []Call{{Method: "Api.Login", Params: map[string]string{"user": c.opt.User, "password": c.opt.Password}}}, "")
	if err != nil {
		return "", err
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := decode(rs[0], &res); err != nil {
		return "", err
	}
	if res.Token == "" {
		return "", errors.New("webapi: Api.Login returned no token")
	}
	c.mu.Lock()
	c.token = res.Token
	c.mu.Unlock()
	return res.Token, nil
}

// Logout ends the session, freeing one of the CPU's few session slots.
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	tok := c.token
	c.token = ""
	c.mu.Unlock()
	if tok == "" {
		return nil
	}
	rs, err := c.batch(ctx, []Call{{Method: "Api.Logout"}}, tok)
	if err != nil {
		return err
	}
	return rs[0].Err
}

// --- Api.* ---------------------------------------------------------------------

// Ping returns the CPU's ping ID, which changes when the CPU restarts. It
// needs no login.
func (c *Client) Ping(ctx context.Context) (string, error) {
	var s string
	err := c.anon(ctx, "Api.Ping", &s)
	return s, err
}

// Version returns the Web API version. It needs no login.
func (c *Client) Version(ctx context.Context) (json.Number, error) {
	var v json.Number
	err := c.anon(ctx, "Api.Version", &v)
	return v, err
}

// Methods lists the methods this CPU's firmware offers. It needs no login.
func (c *Client) Methods(ctx context.Context) ([]string, error) {
	var ms []struct {
		Name string `json:"name"`
	}
	if err := c.anon(ctx, "Api.GetMethods", &ms); err != nil {
		return nil, err
	}
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out, nil
}

// Permissions lists what the logged-in user may do ("read_value",
// "write_value", "change_operating_mode", …).
func (c *Client) Permissions(ctx context.Context) ([]string, error) {
	var ps []struct {
		Name string `json:"name"`
	}
	if err := c.call(ctx, "Api.GetPermissions", nil, &ps); err != nil {
		return nil, err
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
