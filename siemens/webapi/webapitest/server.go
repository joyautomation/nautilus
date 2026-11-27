// Package webapitest is a fake PLC Web API: JSON-RPC 2.0 at /api/jsonrpc
// over TLS, with users, permissions, a symbol table, token expiry on a
// settable clock, the request body limit and the operating mode. It is
// what the Web API client and `naut siemens serve` are tested against
// until a real CPU's web server is on.
//
// It models what the Siemens documentation and Siemens' .NET client say.
// Where the real CPU is not yet measured it makes a choice and says so;
// bench runs (S2b) correct it. Unmeasured choices:
//
//   - an expired token answers PermissionDenied (2);
//   - an oversized body is answered with HTTP 413;
//   - a write of the wrong type or out of range answers InvalidParams
//     (-32602);
//   - DiagnosticBuffer.Browse answers InvalidParams for a missing or empty
//     language, a negative count or any parameter besides language and
//     count (the filters object is not modelled); it accepts any language
//     (a real CPU answers only in its project's languages, error
//     unmeasured); entries come newest first; the buffer holds DiagMax
//     entries (default 3200, the manual's example; a 1510SP's size is
//     unmeasured).
//
// Values: BOOL takes a JSON boolean; the integer types (SINT…ULINT, BYTE…
// LWORD) take a whole number in the type's range; REAL and LREAL any
// number; STRING and WSTRING a string no longer than MaxLength (default
// 254); CHAR a one-character string. NOT modelled, and refused on write
// as unsupported: TIME, LTIME, DATE, TOD, DT, LDT, DTL, arrays as a whole
// and structs. Their JSON encoding on a real CPU is unverified until S2b.
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
	Datatype  string // "bool", "int", "real", "string", "struct", …
	Value     any
	ReadOnly  bool
	MaxLength int // STRING/WSTRING; default 254
}

// DiagEntry is one diagnostic buffer entry in the fake.
type DiagEntry struct {
	Time     time.Time
	Incoming bool // "incoming", else "outgoing"
	TextList int
	Text     int
	Short    string
	Long     string
	Help     string
}

// Server is one fake CPU.
type Server struct {
	Users map[string]User
	Vars  map[string]*Var
	Mode  string // "run" or "stop"; default "run"
	// Methods is what Api.GetMethods returns. Default: every method the
	// fake implements. When set, a method left out answers MethodNotFound
	// (-32601), as on firmware that lacks it.
	Methods []string
	// Diag is the diagnostic buffer, newest first. AddDiag adds to it
	// while the server runs.
	Diag []DiagEntry
	// DiagMax is the buffer's size; default 3200.
	DiagMax int
	// DiagModified is when the buffer last changed; default the newest
	// entry's time.
	DiagModified time.Time
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
	// FailAfter, when > 0, answers HTTP 503 to every request after the
	// FailAfter-th, to test what a caller does when a bulk request is lost.
	FailAfter int

	mu       sync.Mutex
	tokens   map[string]*session
	nextTok  int
	pingID   string
	pingGen  int
	Requests int // HTTP requests served, for asserting batching
	Calls    int // JSON-RPC calls served
	Logins   int // successful Api.Login calls

	loginAttempts int
	writeError    int // when set, every PlcProgram.Write answers this code
	browseError   int // when set, every PlcProgram.Browse answers this code
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

// Set changes a variable's value while the server runs, as the CPU's
// program would. The exported maps are for setup before Start; while the
// server runs, change things only through these methods.
func (s *Server) Set(sym string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x, ok := s.Vars[sym]; ok {
		x.Value = v
	}
}

// Put adds or replaces a variable while the server runs; nil removes it.
func (s *Server) Put(sym string, v *Var) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v == nil {
		delete(s.Vars, sym)
		return
	}
	s.Vars[sym] = v
}

// SetUser adds or replaces a user while the server runs.
func (s *Server) SetUser(name string, u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Users[name] = u
}

// StopAnswering makes every later request fail with HTTP 503, as a CPU
// that dropped off the network would.
func (s *Server) StopAnswering() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FailAfter = s.Requests
	if s.FailAfter == 0 {
		s.FailAfter = -1
	}
}

// FailWrites makes every later PlcProgram.Write answer the CPU error code
// (webapi.CodeSystemIsBusy, say); 0 clears it.
func (s *Server) FailWrites(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeError = code
}

// FailBrowse makes every later PlcProgram.Browse answer the CPU error
// code (webapi.CodeSystemIsBusy, say) while reads and writes still work;
// 0 clears it.
func (s *Server) FailBrowse(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.browseError = code
}

// LoginAttempts counts every Api.Login, successful or not.
func (s *Server) LoginAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loginAttempts
}

// AddDiag puts an entry at the head of the diagnostic buffer, as a CPU
// event would, dropping the oldest beyond DiagMax.
func (s *Server) AddDiag(e DiagEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaults()
	s.Diag = append([]DiagEntry{e}, s.Diag...)
	if len(s.Diag) > s.DiagMax {
		s.Diag = s.Diag[:s.DiagMax]
	}
	s.DiagModified = e.Time
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
	if s.DiagMax == 0 {
		s.DiagMax = 3200
	}
	if s.pingID == "" {
		s.pingGen++
		s.pingID = fmt.Sprintf("ping-%d", s.pingGen)
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
	if s.FailAfter < 0 || s.FailAfter > 0 && s.Requests > s.FailAfter {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
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
	"DiagnosticBuffer.Browse",
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
		s.loginAttempts++
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
		s.Logins++
		return ok(map[string]string{"token": t})
	}

	if !s.offers(q.Method) {
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
		if s.writeError != 0 {
			return fail(s.writeError, "write failed")
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
		val, msg := coerce(v, p.Value)
		if msg != "" {
			return fail(-32602, "Invalid params: "+msg)
		}
		v.Value = val
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
		if s.browseError != 0 {
			return fail(s.browseError, "browse failed")
		}
		return s.browse(resp, p.Var, p.Mode)
	case "DiagnosticBuffer.Browse":
		if !can("read_diagnostics") {
			return fail(2, "Permission denied")
		}
		return s.diagBrowse(resp, q.Params)
	}
	return fail(-32601, "Method not found")
}

// offers reports whether the fake implements the method and, when Methods
// is set, lists it.
func (s *Server) offers(method string) bool {
	ms := implemented
	if s.Methods != nil {
		ms = s.Methods
	}
	for _, m := range ms {
		if m == method {
			for _, i := range implemented {
				if i == method {
					return true
				}
			}
		}
	}
	return false
}

// diagStamp is the CPU's time stamp format: UTC, nanoseconds, "Z".
const diagStamp = "2006-01-02T15:04:05.000000000Z"

func (s *Server) diagBrowse(resp rpcResp, raw json.RawMessage) rpcResp {
	var p struct {
		Language *string `json:"language"`
		Count    *int    `json:"count"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if len(raw) == 0 || dec.Decode(&p) != nil || p.Language == nil || *p.Language == "" ||
		p.Count != nil && *p.Count < 0 {
		resp.Error = &rpcErr{-32602, "Invalid params"}
		return resp
	}
	n := 50
	if p.Count != nil {
		n = *p.Count
	}
	mod := s.DiagModified
	if mod.IsZero() && len(s.Diag) > 0 {
		mod = s.Diag[0].Time
	}
	out := map[string]any{
		"last_modified": mod.UTC().Format(diagStamp),
		"count_current": len(s.Diag),
		"count_max":     s.DiagMax,
	}
	if n == 0 {
		resp.Result = out
		return resp
	}
	if n > len(s.Diag) {
		n = len(s.Diag)
	}
	entries := make([]map[string]any, n)
	for i, e := range s.Diag[:n] {
		status := "outgoing"
		if e.Incoming {
			status = "incoming"
		}
		entries[i] = map[string]any{
			"timestamp":  e.Time.UTC().Format(diagStamp),
			"status":     status,
			"event":      map[string]int{"textlist_id": e.TextList, "text_id": e.Text},
			"short_text": e.Short,
			"long_text":  e.Long,
			"help_text":  e.Help,
		}
	}
	out["entries"] = entries
	out["language"] = *p.Language
	resp.Result = out
	return resp
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

var intRange = map[string][2]float64{
	"sint": {-128, 127}, "int": {-32768, 32767}, "dint": {-2147483648, 2147483647},
	"lint":  {-9223372036854775808, 9223372036854775807},
	"usint": {0, 255}, "uint": {0, 65535}, "udint": {0, 4294967295}, "ulint": {0, 18446744073709551615},
	"byte": {0, 255}, "word": {0, 65535}, "dword": {0, 4294967295}, "lword": {0, 18446744073709551615},
}

// coerce checks a written JSON value against the variable's type and
// returns the value to store, or why it is refused.
func coerce(v *Var, x any) (any, string) {
	t := strings.ToLower(v.Datatype)
	switch t {
	case "bool":
		if b, ok := x.(bool); ok {
			return b, ""
		}
		return nil, "BOOL takes true or false"
	case "real", "lreal":
		if f, ok := x.(float64); ok {
			return f, ""
		}
		return nil, strings.ToUpper(t) + " takes a number"
	case "string", "wstring":
		str, ok := x.(string)
		if !ok {
			return nil, strings.ToUpper(t) + " takes a string"
		}
		max := v.MaxLength
		if max == 0 {
			max = 254
		}
		if len([]rune(str)) > max {
			return nil, fmt.Sprintf("%s is at most %d characters", strings.ToUpper(t), max)
		}
		return str, ""
	case "char", "wchar":
		if str, ok := x.(string); ok && len([]rune(str)) == 1 {
			return str, ""
		}
		return nil, "CHAR takes a one-character string"
	}
	if r, ok := intRange[t]; ok {
		f, isNum := x.(float64)
		if !isNum || f != float64(int64(f)) && t != "ulint" && t != "lword" || f < r[0] || f > r[1] {
			return nil, fmt.Sprintf("%s takes a whole number from %.0f to %.0f", strings.ToUpper(t), r[0], r[1])
		}
		return f, ""
	}
	return nil, "the fake does not model writing " + strings.ToUpper(t) + " (encoding unverified until S2b)"
}
