// Package webapitest is a fake PLC Web API: JSON-RPC 2.0 at /api/jsonrpc
// over TLS, with users, permissions, a symbol table, token expiry on a
// settable clock, the request body limit and the operating mode. It is
// what the Web API client and `naut siemens serve` are tested against
// until a real CPU's web server is on.
//
// It models what the Siemens documentation and Siemens' .NET client say.
// Where the real CPU is not yet measured (the code an expired token
// answers, the reply to an oversized body) it makes a choice and says so;
// bench runs correct it.
package webapitest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// User is one web server user.
type User struct {
	Password    string
	Permissions []string // "read_value", "write_value", "change_operating_mode", …
}

// Var is one variable in the fake CPU's symbol table, keyed by its full
// symbol (`"Tags".Level`). Value is what a read returns.
type Var struct {
	Datatype string // "bool", "int", "real", "struct", …
	Value    any
	ReadOnly bool
}

// Server is one fake CPU.
type Server struct {
	Users map[string]User
	Vars  map[string]*Var
	Mode  string // "run" or "stop"; default "run"
	// Methods is what Api.GetMethods returns. Default: every method the
	// fake implements.
	Methods []string
	// MaxBody is the body limit; default 64 kB. A larger body is answered
	// with HTTP 413 (the real CPU's answer is unmeasured).
	MaxBody int
	// IdleTimeout expires a token after this long without an
	// authenticated call; default 120 s. An expired token answers
	// PermissionDenied (unmeasured on a real CPU).
	IdleTimeout time.Duration
	// MaxSessions bounds concurrent tokens; default 50 (a 1510-class CPU).
	// Beyond it Api.Login answers NoResources.
	MaxSessions int
	// Now is the clock; default time.Now.
	Now func() time.Time

	mu       sync.Mutex
	tokens   map[string]*session
	nextTok  int
	pingID   string
	Requests int // HTTP requests served, for asserting batching
	Calls    int // JSON-RPC calls served
}

type session struct {
	user     string
	lastUsed time.Time
}

// New returns a fake CPU in RUN with no users and no variables.
func New() *Server {
	return &Server{Users: map[string]User{}, Vars: map[string]*Var{}}
}

// Start serves the fake over TLS on a loopback port until the test ends.
// It returns the base URL and the SHA-256 of its certificate, to pin.
func (s *Server) Start(t testing.TB) (url, certSHA256 string) {
	t.Helper()
	srv := httptest.NewTLSServer(s)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	return srv.URL, hex.EncodeToString(sum[:])
}

// Expire forgets every token, as a CPU restart would.
func (s *Server) Expire() {
	s.mu.Lock()
	s.tokens = nil
	s.pingID = ""
	s.mu.Unlock()
}

// Sessions is the number of live tokens.
func (s *Server) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reap()
	return len(s.tokens)
}

// Value returns a variable's current value.
func (s *Server) Value(sym string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.Vars[sym]; ok {
		return v.Value
	}
	return nil
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) defaults() {
	if s.tokens == nil {
		s.tokens = map[string]*session{}
	}
	if s.Mode == "" {
		s.Mode = "run"
	}
	if s.MaxBody == 0 {
		s.MaxBody = 64 * 1024
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = 120 * time.Second
	}
	if s.MaxSessions == 0 {
		s.MaxSessions = 50
	}
	if s.pingID == "" {
		s.pingID = fmt.Sprintf("ping-%d", s.now().UnixNano())
	}
}

func (s *Server) reap() {
	now := s.now()
	for t, ss := range s.tokens {
		if now.Sub(ss.lastUsed) > s.IdleTimeout {
			delete(s.tokens, t)
		}
	}
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      any             `json:"id"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResp struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      any     `json:"id"`
	Result  any     `json:"result,omitempty"`
	Error   *rpcErr `json:"error,omitempty"`
}

// ServeHTTP answers /api/jsonrpc.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/jsonrpc" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaults()
	s.Requests++
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(s.MaxBody)+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > s.MaxBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	token := r.Header.Get("X-Auth-Token")
	w.Header().Set("Content-Type", "application/json")
	if trimmed := strings.TrimSpace(string(body)); strings.HasPrefix(trimmed, "[") {
		var reqs []rpcReq
		if err := json.Unmarshal(body, &reqs); err != nil {
			_ = json.NewEncoder(w).Encode(rpcResp{JSONRPC: "2.0", Error: &rpcErr{-32700, "Parse error"}})
			return
		}
		out := make([]rpcResp, len(reqs))
		for i, q := range reqs {
			out[i] = s.dispatch(q, token)
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	var q rpcReq
	if err := json.Unmarshal(body, &q); err != nil {
		_ = json.NewEncoder(w).Encode(rpcResp{JSONRPC: "2.0", Error: &rpcErr{-32700, "Parse error"}})
		return
	}
	_ = json.NewEncoder(w).Encode(s.dispatch(q, token))
}

var implemented = []string{
	"Api.GetPermissions", "Api.Login", "Api.Logout", "Api.Ping", "Api.Version", "Api.GetMethods",
	"PlcProgram.Browse", "PlcProgram.Read", "PlcProgram.Write",
	"Plc.ReadOperatingMode", "Plc.RequestChangeOperatingMode",
}

func (s *Server) dispatch(q rpcReq, token string) rpcResp {
	s.Calls++
	resp := rpcResp{JSONRPC: "2.0", ID: q.ID}
	fail := func(code int, msg string) rpcResp { resp.Error = &rpcErr{code, msg}; return resp }
	ok := func(v any) rpcResp { resp.Result = v; return resp }

	switch q.Method {
	case "Api.Ping":
		return ok(s.pingID)
	case "Api.Version":
		return ok(json.Number("4"))
	case "Api.GetMethods":
		ms := s.Methods
		if ms == nil {
			ms = implemented
		}
		out := make([]map[string]string, len(ms))
		for i, m := range ms {
			out[i] = map[string]string{"name": m}
		}
		return ok(out)
	case "Api.Login":
		var p struct {
			User     string `json:"user"`
			Password string `json:"password"`
		}
		if json.Unmarshal(q.Params, &p) != nil {
			return fail(-32602, "Invalid params")
		}
		if token != "" {
			if _, live := s.tokens[token]; live {
				return fail(101, "Already authenticated")
			}
		}
		u, known := s.Users[p.User]
		if !known || u.Password != p.Password {
			return fail(100, "Login failed")
		}
		s.reap()
		if len(s.tokens) >= s.MaxSessions {
			return fail(4, "No resources")
		}
		s.nextTok++
		t := fmt.Sprintf("tok%04d-%s", s.nextTok, strings.Repeat("x", 19))
		s.tokens[t] = &session{user: p.User, lastUsed: s.now()}
		return ok(map[string]string{"token": t})
	}

	if !q.known() {
		return fail(-32601, "Method not found")
	}
	// Everything below needs a live token.
	s.reap()
	ss, live := s.tokens[token]
	if !live {
		return fail(2, "Permission denied")
	}
	ss.lastUsed = s.now()
	user := s.Users[ss.user]
	can := func(p string) bool {
		for _, x := range user.Permissions {
			if x == p {
				return true
			}
		}
		return false
	}

	switch q.Method {
	case "Api.Logout":
		delete(s.tokens, token)
		return ok(true)
	case "Api.GetPermissions":
		out := []map[string]string{}
		for _, p := range user.Permissions {
			out = append(out, map[string]string{"name": p})
		}
		return ok(out)
	case "Plc.ReadOperatingMode":
		return ok(s.Mode)
	case "Plc.RequestChangeOperatingMode":
		if !can("change_operating_mode") {
			return fail(2, "Permission denied")
		}
		var p struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal(q.Params, &p) != nil || (p.Mode != "run" && p.Mode != "stop") {
			return fail(-32602, "Invalid params")
		}
		s.Mode = p.Mode
		return ok(true)
	case "PlcProgram.Read":
		if !can("read_value") {
			return fail(2, "Permission denied")
		}
		var p struct {
			Var string `json:"var"`
		}
		if json.Unmarshal(q.Params, &p) != nil {
			return fail(-32602, "Invalid params")
		}
		v, found := s.Vars[p.Var]
		if !found {
			return fail(200, "Address does not exist")
		}
		if v.Datatype == "struct" {
			return fail(204, "Unsupported address")
		}
		return ok(v.Value)
	case "PlcProgram.Write":
		if !can("write_value") {
			return fail(2, "Permission denied")
		}
		var p struct {
			Var   string `json:"var"`
			Value any    `json:"value"`
		}
		if json.Unmarshal(q.Params, &p) != nil {
			return fail(-32602, "Invalid params")
		}
		v, found := s.Vars[p.Var]
		if !found {
			return fail(200, "Address does not exist")
		}
		if v.ReadOnly {
			return fail(205, "Address is read-only")
		}
		if v.Datatype == "bool" {
			if _, isBool := p.Value.(bool); !isBool {
				return fail(-32602, "Invalid params")
			}
		}
		v.Value = p.Value
		return ok(true)
	case "PlcProgram.Browse":
		if !can("read_value") {
			return fail(2, "Permission denied")
		}
		var p struct {
			Var  string `json:"var"`
			Mode string `json:"mode"`
		}
		if json.Unmarshal(q.Params, &p) != nil || (p.Mode != "children" && p.Mode != "var") {
			return fail(-32602, "Invalid params")
		}
		return s.browse(resp, p.Var, p.Mode)
	}
	return fail(-32601, "Method not found")
}

// known reports whether the fake implements the method.
func (q rpcReq) known() bool {
	for _, m := range implemented {
		if m == q.Method {
			return true
		}
	}
	return false
}

func (s *Server) browse(resp rpcResp, v, mode string) rpcResp {
	entry := func(sym string, x *Var) map[string]any {
		name := sym
		if i := strings.LastIndex(sym, "."); i >= 0 {
			name = sym[i+1:]
		}
		return map[string]any{"name": strings.Trim(name, `"`), "datatype": x.Datatype,
			"has_children": x.Datatype == "struct", "read_only": x.ReadOnly}
	}
	if mode == "var" {
		x, found := s.Vars[v]
		if !found {
			resp.Error = &rpcErr{200, "Address does not exist"}
			return resp
		}
		resp.Result = []map[string]any{entry(v, x)}
		return resp
	}
	if v != "" {
		x, found := s.Vars[v]
		if !found {
			resp.Error = &rpcErr{200, "Address does not exist"}
			return resp
		}
		if x.Datatype != "struct" {
			resp.Error = &rpcErr{202, "Variable is not a structure"}
			return resp
		}
	}
	var syms []string
	for sym := range s.Vars {
		parent := ""
		if i := strings.LastIndex(sym, "."); i >= 0 {
			parent = sym[:i]
		}
		if parent == v {
			syms = append(syms, sym)
		}
	}
	sort.Strings(syms)
	out := []map[string]any{}
	for _, sym := range syms {
		out = append(out, entry(sym, s.Vars[sym]))
	}
	resp.Result = out
	return resp
}
