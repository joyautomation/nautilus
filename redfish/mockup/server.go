// server.go serves a Tree over HTTP the way a BMC would, closely enough
// for the driver's tests and for `naut redfish serve` on a laptop: GET of
// any recorded resource, the session login/logout dance with X-Auth-Token,
// basic auth, the ComputerSystem.Reset action (recorded, and reflected in
// PowerState), and the fault knobs a test needs — a 404 on one path, a
// patched value, a restart that forgets every session.
package mockup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Auth selects what the server demands.
type Auth string

const (
	// AuthNone serves everything to anyone (DMTF's mockup server does this).
	AuthNone Auth = ""
	// AuthSession accepts a session token or basic auth — a real BMC.
	AuthSession Auth = "session"
	// AuthBasic accepts basic auth only and answers the session POST with
	// 405, like a BMC that does not do sessions.
	AuthBasic Auth = "basic"
)

// Options configure a Server.
type Options struct {
	Auth     Auth
	User     string
	Password string
	// Latency delays every response (a slow BMC).
	Latency time.Duration
}

// Action is one POST to an Actions/ target the server accepted.
type Action struct {
	Path string
	Body map[string]any
}

// Server is the stand-in BMC.
type Server struct {
	opts Options

	mu       sync.Mutex
	tree     Tree
	fail     map[string]int      // uri → status to answer instead
	sessions map[string]string   // token → session uri
	actions  []Action            // accepted action POSTs
	gets     map[string]int      // uri → GET count
	patched  map[string][]string // uri → patched paths (diagnostics)
	logins   int

	ln   net.Listener
	srv  *http.Server
	addr string
}

// New wraps a tree; nothing listens until Start.
func New(t Tree, opts Options) *Server {
	cp := make(Tree, len(t))
	for k, v := range t {
		cp[k] = append(json.RawMessage(nil), v...)
	}
	return &Server{
		opts:     opts,
		tree:     cp,
		fail:     map[string]int{},
		sessions: map[string]string{},
		gets:     map[string]int{},
		patched:  map[string][]string{},
	}
}

// Start listens on addr ("127.0.0.1:0" for a free port). A restarted
// server reuses the address it had.
func (s *Server) Start(addr string) error {
	s.mu.Lock()
	if s.addr != "" && (addr == "" || strings.HasSuffix(addr, ":0")) {
		addr = s.addr
	}
	s.mu.Unlock()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	var ln net.Listener
	var err error
	// A just-closed port can take a moment to be reusable on some systems.
	for i := 0; i < 50; i++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second}
	s.mu.Lock()
	s.ln, s.srv, s.addr = ln, srv, ln.Addr().String()
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// Stop closes the listener and every connection.
func (s *Server) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv, s.ln = nil, nil
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// Restart stops and starts on the same address, forgetting every session
// — what a BMC reboot does to a client's token.
func (s *Server) Restart() error {
	s.Stop()
	s.mu.Lock()
	s.sessions = map[string]string{}
	s.mu.Unlock()
	return s.Start("")
}

// Addr is host:port; URL is the base URL a source's host: takes.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// URL is "http://host:port".
func (s *Server) URL() string { return "http://" + s.Addr() }

// Fail makes GETs of uri answer status (404, 503, …); 0 restores it.
func (s *Server) Fail(uri string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	uri = Normalize(uri)
	if status == 0 {
		delete(s.fail, uri)
	} else {
		s.fail[uri] = status
	}
}

// Patch sets one property in a resource, by a dotted path of object keys
// (no selectors: arrays are addressed by numeric segment, "Fans.0.Reading",
// since this is test plumbing, not a binding). value nil deletes it.
func (s *Server) Patch(uri, path string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	uri = Normalize(uri)
	raw, ok := s.tree[uri]
	if !ok {
		return fmt.Errorf("%s: %w", uri, ErrNotFound)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	segs := strings.Split(path, ".")
	cur := doc
	for i, seg := range segs {
		last := i == len(segs)-1
		switch c := cur.(type) {
		case map[string]any:
			if last {
				if value == nil {
					delete(c, seg)
				} else {
					c[seg] = value
				}
				break
			}
			cur = c[seg]
		case []any:
			var n int
			if _, err := fmt.Sscanf(seg, "%d", &n); err != nil || n < 0 || n >= len(c) {
				return fmt.Errorf("%s: %s: no element %q", uri, path, seg)
			}
			if last {
				c[n] = value
				break
			}
			cur = c[n]
		default:
			return fmt.Errorf("%s: %s: %q is not an object or array", uri, path, seg)
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	s.tree[uri] = out
	s.patched[uri] = append(s.patched[uri], path)
	return nil
}

// Actions returns the accepted action POSTs, in order.
func (s *Server) Actions() []Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Action(nil), s.actions...)
}

// Gets reports how many times uri was fetched.
func (s *Server) Gets(uri string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets[Normalize(uri)]
}

// Logins reports how many sessions have been created since New.
func (s *Server) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

// Tree returns a copy of the current tree (patches applied).
func (s *Server) Tree() Tree {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(Tree, len(s.tree))
	for k, v := range s.tree {
		cp[k] = v
	}
	return cp
}

// ServeHTTP answers one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.opts.Latency > 0 {
		time.Sleep(s.opts.Latency)
	}
	uri := Normalize(r.URL.Path)
	switch {
	case uri == "/redfish" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"v1": Root + "/"})
		return
	case uri == Normalize(sessionsPath) && r.Method == http.MethodPost:
		s.login(w, r)
		return
	}
	// The service root is readable without credentials (Redfish spec
	// §13.3); everything else is not, when auth is on.
	if uri != Root && !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "Base.1.0.NoValidSession", "There is no valid session established with the implementation.")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.get(w, uri)
	case http.MethodPost:
		s.post(w, r, uri)
	case http.MethodDelete:
		s.mu.Lock()
		for tok, su := range s.sessions {
			if su == uri {
				delete(s.sessions, tok)
			}
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "Base.1.0.OperationNotAllowed", "The HTTP method is not allowed on this URI.")
	}
}

const sessionsPath = Root + "/SessionService/Sessions"

func (s *Server) get(w http.ResponseWriter, uri string) {
	s.mu.Lock()
	s.gets[uri]++
	status := s.fail[uri]
	raw, ok := s.tree[uri]
	s.mu.Unlock()
	if status != 0 {
		writeError(w, status, "Base.1.0.InternalError", fmt.Sprintf("injected HTTP %d for %s", status, uri))
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "Base.1.0.ResourceMissingAtURI", fmt.Sprintf("The resource at the URI %s was not found.", uri))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("OData-Version", "4.0")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.opts.Auth == AuthBasic {
		writeError(w, http.StatusMethodNotAllowed, "Base.1.0.OperationNotAllowed", "Sessions are not supported; use basic authentication.")
		return
	}
	var creds struct{ UserName, Password string }
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err := json.Unmarshal(body, &creds); err != nil {
		writeError(w, http.StatusBadRequest, "Base.1.0.MalformedJSON", "The request body submitted was malformed JSON.")
		return
	}
	if s.opts.Auth != AuthNone && (creds.UserName != s.opts.User || creds.Password != s.opts.Password) {
		writeError(w, http.StatusUnauthorized, "Base.1.0.ResourceAtUriUnauthorized", "While accessing the resource at /redfish/v1/SessionService/Sessions, the service received an authorization error.")
		return
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	tok := hex.EncodeToString(b[:])
	s.mu.Lock()
	s.logins++
	id := fmt.Sprintf("%d", s.logins)
	uri := sessionsPath + "/" + id
	s.sessions[tok] = uri
	s.mu.Unlock()
	w.Header().Set("X-Auth-Token", tok)
	w.Header().Set("Location", uri)
	writeJSON(w, http.StatusCreated, map[string]any{
		"@odata.id": uri, "@odata.type": "#Session.v1_0_0.Session", "Id": id, "Name": "User Session", "UserName": creds.UserName,
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.opts.Auth == AuthNone {
		return true
	}
	if tok := r.Header.Get("X-Auth-Token"); tok != "" {
		s.mu.Lock()
		_, ok := s.sessions[tok]
		s.mu.Unlock()
		return ok && s.opts.Auth == AuthSession
	}
	u, p, ok := r.BasicAuth()
	return ok && u == s.opts.User && p == s.opts.Password
}

// post accepts an action whose target a recorded resource advertises, and
// applies ComputerSystem.Reset to PowerState so a read-back follows.
func (s *Server) post(w http.ResponseWriter, r *http.Request, uri string) {
	owner, _, ok := strings.Cut(uri, "/Actions/")
	if !ok {
		writeError(w, http.StatusMethodNotAllowed, "Base.1.0.OperationNotAllowed", "POST is only accepted on actions here.")
		return
	}
	s.mu.Lock()
	raw, found := s.tree[owner]
	s.mu.Unlock()
	if !found || !strings.Contains(string(raw), `"`+uri+`"`) {
		writeError(w, http.StatusNotFound, "Base.1.0.ActionNotSupported", fmt.Sprintf("The action %s is not supported by the resource.", uri))
		return
	}
	var body map[string]any
	raw2, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if len(raw2) > 0 {
		if err := json.Unmarshal(raw2, &body); err != nil {
			writeError(w, http.StatusBadRequest, "Base.1.0.MalformedJSON", "The request body submitted was malformed JSON.")
			return
		}
	}
	if strings.HasSuffix(uri, "/ComputerSystem.Reset") {
		switch body["ResetType"] {
		case "On", "ForceOn", "GracefulRestart", "ForceRestart", "PowerCycle":
			_ = s.Patch(owner, "PowerState", "On")
		case "ForceOff", "GracefulShutdown":
			_ = s.Patch(owner, "PowerState", "Off")
		default:
			writeError(w, http.StatusBadRequest, "Base.1.0.ActionParameterValueNotInList", fmt.Sprintf("ResetType %v is not allowed.", body["ResetType"]))
			return
		}
	}
	s.mu.Lock()
	s.actions = append(s.actions, Action{Path: uri, Body: body}) // accepted ones only
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("OData-Version", "4.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, id, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": id, "message": msg,
		"@Message.ExtendedInfo": []any{map[string]any{"MessageId": id, "Message": msg}},
	}})
}

// Sessions lists the live session URIs (sorted) — a test checks a clean
// Stop logged out.
func (s *Server) Sessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sessions))
	for _, u := range s.sessions {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}
