package channeltest

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeCA(t testing.TB, srv *httptest.Server) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "ca.pem")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// Request is one call the Provider received.
type Request struct {
	Kind    string // "sms" or "voice"
	Account string // from the path
	User    string // basic auth
	Token   string
	To      string
	From    string
	Body    string
	Twiml   string
}

// Provider fakes the two Twilio endpoints over TLS.
type Provider struct {
	URL    string
	CAFile string

	srv      *httptest.Server
	mu       sync.Mutex
	reqs     []Request
	status   int
	body     string
	redirect string
}

// NewProvider starts it; t closes it. Responses are 201 until SetStatus.
func NewProvider(t testing.TB) *Provider {
	t.Helper()
	p := &Provider{status: 201, body: "{}"}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.srv.Close)
	p.URL = p.srv.URL
	p.CAFile = writeCA(t, p.srv)
	return p
}

func (p *Provider) handle(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	rq := Request{To: r.PostForm.Get("To"), From: r.PostForm.Get("From"),
		Body: r.PostForm.Get("Body"), Twiml: r.PostForm.Get("Twiml")}
	if len(parts) == 4 && parts[0] == "2010-04-01" && parts[1] == "Accounts" {
		rq.Account = parts[2]
		switch parts[3] {
		case "Messages.json":
			rq.Kind = "sms"
		case "Calls.json":
			rq.Kind = "voice"
		}
	}
	rq.User, rq.Token, _ = r.BasicAuth()
	p.mu.Lock()
	p.reqs = append(p.reqs, rq)
	status, body, redir := p.status, p.body, p.redirect
	p.mu.Unlock()
	if rq.Kind == "" {
		http.NotFound(w, r)
		return
	}
	if redir != "" {
		http.Redirect(w, r, redir, http.StatusFound)
		return
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// SetStatus sets the status and body of every later response.
func (p *Provider) SetStatus(code int, body string) {
	p.mu.Lock()
	p.status, p.body = code, body
	p.mu.Unlock()
}

// SetRedirect makes every later response a 302 to location.
func (p *Provider) SetRedirect(location string) {
	p.mu.Lock()
	p.redirect = location
	p.mu.Unlock()
}

// Requests returns what was received.
func (p *Provider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.reqs...)
}

// Hits counts requests received.
func (p *Provider) Hits() int { return len(p.Requests()) }

// Hook is one webhook call.
type Hook struct {
	Header http.Header
	Body   []byte
}

// Webhook fakes a webhook receiver over TLS.
type Webhook struct {
	URL    string
	CAFile string

	srv    *httptest.Server
	mu     sync.Mutex
	hooks  []Hook
	status int
}

// NewWebhook starts it; t closes it. It answers 200.
func NewWebhook(t testing.TB) *Webhook {
	t.Helper()
	w := &Webhook{status: 200}
	w.srv = httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.hooks = append(w.hooks, Hook{Header: r.Header.Clone(), Body: b})
		st := w.status
		w.mu.Unlock()
		rw.WriteHeader(st)
	}))
	t.Cleanup(w.srv.Close)
	w.URL = w.srv.URL
	w.CAFile = writeCA(t, w.srv)
	return w
}

// SetStatus sets the status of every later response.
func (w *Webhook) SetStatus(code int) {
	w.mu.Lock()
	w.status = code
	w.mu.Unlock()
}

// Hooks returns what was received.
func (w *Webhook) Hooks() []Hook {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Hook(nil), w.hooks...)
}
