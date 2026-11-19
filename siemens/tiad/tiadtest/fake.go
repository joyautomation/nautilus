// Package tiadtest is an in-memory tiad: the agent's HTTP contract
// (docs/design/siemens-ide.md §3) without TIA Portal behind it. It enforces
// the rules a caller can get wrong (the token, the work-dir sandbox, the
// target allowlist, station validation, compile-before-download and the
// download decision rule) and scripts what only TIA knows: the
// pre-download questions, the compile result and the online compare.
//
// It is the executable half of the contract. The C# agent must answer the
// same requests the same way; where the two disagree, the brief decides
// and one of them is fixed.
package tiadtest

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// Fake is one fake agent. Set the exported fields before serving; read
// what happened through the accessor methods.
type Fake struct {
	// Token is the bearer token every request must carry. New sets a
	// random one; the agent never runs without a token, and neither does
	// the fake (an empty Token refuses every request).
	Token string
	// Targets are the CPU addresses downloads may go to (TIAD_TARGETS).
	Targets []string
	// Gates are what probe reports. Empty means every gate passes. Any
	// gate that blocks (tiad.Gate.Blocks: failed, or a state it does not
	// know) makes every TIA call fail with kind "gate"; an unknown one
	// does not. Set them with SetGates, which derives OK from State.
	Gates []tiad.Gate
	// PreDownload scripts TIA's pre-download checks. Nil means none.
	PreDownload func(tiad.DownloadRequest) []tiad.Question
	// CompileResult is what compile returns. Zero means success. A result
	// with errors fails the call with kind "compile_failed".
	CompileResult tiad.CompileResult
	// CompareResult, when set, is what compare returns. Otherwise the
	// project equals the CPU exactly when this session's project is the
	// last one downloaded to the station's CPU and has not changed since.
	CompareResult *tiad.CompareResult

	mu        sync.Mutex
	files     map[string][]byte
	gsd       map[string]bool
	sessions  map[string]*session
	next      int
	downloads []Download
	loaded    map[string]string // CPU address → session that last downloaded
	claims    map[string]string // CPU address → the session that claimed it (one project per CPU)
	mux       *http.ServeMux
	once      sync.Once
}

type session struct {
	id, project string
	// guard is the directory a created project owns, "projects/<name>/".
	guard    string
	station  *tiad.Station
	blocks   []tiad.Block
	compiled bool
	// changed is true until a download loads the project as it is.
	changed bool
	// uploaded is true after an upload: the project no longer matches
	// its spec until one is applied again.
	uploaded bool
}

// Download records one real download. Secrets are never recorded.
type Download struct {
	Session string
	Request tiad.DownloadRequest
	State   string
}

// New returns an empty fake with a random token.
func New() *Fake { return &Fake{Token: RandomToken()} }

// RandomToken is a fresh bearer token.
func RandomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Serve starts the fake on a loopback port, closed when the test ends, and
// returns a client for it.
func (f *Fake) Serve(t testing.TB) (*httptest.Server, *tiad.Client) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, tiad.New(srv.URL, f.Token)
}

// SetGates replaces what probe reports while the fake is serving. Nil
// means every gate passes. OK is derived from State, as the agent derives
// it: true only in state ok.
func (f *Fake) SetGates(gates []tiad.Gate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Gates = append([]tiad.Gate(nil), gates...)
	for i := range f.Gates {
		f.Gates[i].OK = f.Gates[i].State == tiad.GateOK
	}
}

// SetCompileResult replaces what compile returns while the fake is
// serving.
func (f *Fake) SetCompileResult(r tiad.CompileResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CompileResult = r
}

// SetQuestions makes every download ask qs while the fake is serving.
func (f *Fake) SetQuestions(qs []tiad.Question) {
	f.mu.Lock()
	defer f.mu.Unlock()
	qs = append([]tiad.Question(nil), qs...)
	f.PreDownload = func(tiad.DownloadRequest) []tiad.Question { return append([]tiad.Question(nil), qs...) }
}

// Downloads returns every real download so far.
func (f *Fake) Downloads() []Download {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Download(nil), f.downloads...)
}

// File returns a work-dir file.
func (f *Fake) File(p string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[p]
	return b, ok
}

// Station returns the station spec a session's project holds.
func (f *Fake) Station(sessionID string) (tiad.Station, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok || s.station == nil {
		return tiad.Station{}, false
	}
	return *s.station, true
}

// Blocks returns the blocks a session's project holds, sorted by name.
func (f *Fake) Blocks(sessionID string) []tiad.Block {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil
	}
	return append([]tiad.Block(nil), s.blocks...)
}

// --- HTTP ----------------------------------------------------------------------

type failure struct {
	status int
	kind   string
	msg    string
	data   any
}

func fail(status int, kind, f string, a ...any) *failure {
	return &failure{status: status, kind: kind, msg: fmt.Sprintf(f, a...)}
}

type handler func(r *http.Request) (any, *failure)

func (f *Fake) init() {
	f.once.Do(func() {
		f.files = map[string][]byte{}
		f.gsd = map[string]bool{}
		f.sessions = map[string]*session{}
		f.loaded = map[string]string{}
		f.claims = map[string]string{}
		m := http.NewServeMux()
		m.HandleFunc("GET /v1/health", f.wrap(f.health, false))
		m.HandleFunc("GET /v1/probe", f.wrap(f.probe, false))
		m.HandleFunc("GET /v1/files", f.wrap(f.listFiles, false))
		m.HandleFunc("PUT /v1/files/{path...}", f.wrap(f.putFile, false))
		m.HandleFunc("GET /v1/files/{path...}", f.getFile)
		m.HandleFunc("DELETE /v1/files/{path...}", f.wrap(f.deleteFile, false))
		m.HandleFunc("POST /v1/gsd/install", f.wrap(f.installGSD, true))
		m.HandleFunc("POST /v1/projects", f.wrap(f.createProject, true))
		m.HandleFunc("POST /v1/sessions", f.wrap(f.openSession, true))
		m.HandleFunc("GET /v1/sessions", f.wrap(f.listSessions, false))
		m.HandleFunc("DELETE /v1/sessions/{id}", f.wrap(f.closeSession, false))
		m.HandleFunc("PUT /v1/sessions/{id}/station", f.wrap(f.applyStation, true))
		m.HandleFunc("POST /v1/sessions/{id}/software", f.wrap(f.software, true))
		m.HandleFunc("POST /v1/sessions/{id}/compile", f.wrap(f.compile, true))
		m.HandleFunc("POST /v1/sessions/{id}/download", f.wrap(f.download, true))
		m.HandleFunc("POST /v1/sessions/{id}/compare", f.wrap(f.compare, true))
		m.HandleFunc("POST /v1/sessions/{id}/export", f.wrap(f.export, true))
		m.HandleFunc("POST /v1/sessions/{id}/upload", f.wrap(f.upload, true))
		m.HandleFunc("POST /v1/sessions/{id}/save", f.wrap(f.save, true))
		f.mux = m
	})
}

// ServeHTTP makes the fake an http.Handler.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.init()
	// No browser ever calls the agent.
	if r.Header.Get("Origin") != "" {
		reply(w, nil, fail(http.StatusForbidden, tiad.KindUnauthorized, "a request with an Origin header (a browser) is refused"))
		return
	}
	if f.Token == "" || r.Header.Get("Authorization") != "Bearer "+f.Token {
		reply(w, nil, fail(http.StatusUnauthorized, tiad.KindUnauthorized, "missing or wrong token"))
		return
	}
	f.mux.ServeHTTP(w, r)
}

func (f *Fake) wrap(h handler, needsTIA bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if needsTIA {
			for _, g := range f.Gates {
				if g.Blocks() {
					reply(w, nil, fail(http.StatusPreconditionFailed, tiad.KindGate, "gate %s: %s", g.Name, g.Detail))
					return
				}
			}
		}
		data, fl := h(r)
		reply(w, data, fl)
	}
}

func reply(w http.ResponseWriter, data any, fl *failure) {
	w.Header().Set("Content-Type", "application/json")
	env := map[string]any{"ok": fl == nil, "events": []tiad.Event{}}
	status := http.StatusOK
	if fl != nil {
		status = fl.status
		env["error"] = map[string]any{"kind": fl.kind, "message": fl.msg, "fatal": false}
		data = fl.data
	}
	if data != nil {
		env["data"] = data
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// contentType refuses a body of any other media type.
func contentType(r *http.Request, want string) *failure {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.EqualFold(ct, want) {
		return fail(http.StatusUnsupportedMediaType, tiad.KindBadRequest, "Content-Type must be %s", want)
	}
	return nil
}

func decode(r *http.Request, v any) *failure {
	if fl := contentType(r, "application/json"); fl != nil {
		return fl
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "body: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "body: %v", err)
	}
	// Decode reads one value and leaves the rest: a body is one document.
	if _, err := dec.Token(); err != io.EOF {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "body: invalid character after the top-level value")
	}
	// encoding/json matches field names case-insensitively, so
	// "stopCPU" would answer stopCpu. The agent matches exactly, and so
	// must the fake: a misspelt decision is never an answer.
	// A key twice is refused: "stopCpu": false, "stopCpu": true must
	// never mean yes, whichever one a parser keeps.
	if err := duplicateKey(b); err != nil {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "body: %v", err)
	}
	var raw any
	_ = json.Unmarshal(b, &raw)
	if err := exactKeys(raw, reflect.TypeOf(v), ""); err != nil {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "body: json: %v", err)
	}
	return nil
}

// duplicateKey returns an error for the first object key that appears
// twice in one object, compared decoded.
func duplicateKey(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	type frame struct {
		object  bool
		wantKey bool
		keys    map[string]bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil // the decode proper reports syntax errors
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.object && top.wantKey {
			if k, ok := tok.(string); ok {
				if top.keys[k] {
					return fmt.Errorf("duplicate key %q", k)
				}
				top.keys[k] = true
				top.wantKey = false
				continue
			}
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{object: true, wantKey: true, keys: map[string]bool{}})
			continue
		case json.Delim('['):
			stack = append(stack, &frame{})
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		}
		// A value ended: the enclosing object wants its next key.
		if len(stack) > 0 && stack[len(stack)-1].object {
			stack[len(stack)-1].wantKey = true
		}
	}
}

// exactKeys checks that every object key in raw is exactly a JSON field
// name of t, at every depth.
func exactKeys(raw any, t reflect.Type, at string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch v := raw.(type) {
	case map[string]any:
		switch t.Kind() {
		case reflect.Map:
			for k, x := range v {
				if err := exactKeys(x, t.Elem(), at+k+"."); err != nil {
					return err
				}
			}
		case reflect.Struct:
			for k, x := range v {
				f, ok := fieldByTag(t, k)
				if !ok {
					return fmt.Errorf("unknown field %q", at+k)
				}
				if err := exactKeys(x, f.Type, at+k+"."); err != nil {
					return err
				}
			}
		}
	case []any:
		if t.Kind() == reflect.Slice {
			for _, x := range v {
				if err := exactKeys(x, t.Elem(), at); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func fieldByTag(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" {
			tag = f.Name
		}
		if tag == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// cleanPath enforces the work-dir sandbox with the client's own rule.
func cleanPath(p string) (string, *failure) {
	c, err := tiad.CleanPath(p)
	if err != nil {
		return "", fail(http.StatusBadRequest, tiad.KindBadRequest, "%v", err)
	}
	return c, nil
}

// --- handlers -----------------------------------------------------------------

func (f *Fake) health(*http.Request) (any, *failure) {
	return tiad.Health{Version: "fake", TIA: "V21", Openness: "V21", Sessions: len(f.sessions), Targets: f.Targets}, nil
}

func (f *Fake) probe(*http.Request) (any, *failure) {
	gates := f.Gates
	if len(gates) == 0 {
		for _, n := range []string{"tia", "openness-group", "openness-allowlist", "licence", "workdir"} {
			gates = append(gates, tiad.Gate{Name: n, OK: true, State: tiad.GateOK})
		}
	}
	usable := true
	for _, g := range gates {
		usable = usable && !g.Blocks()
	}
	return tiad.Probe{Usable: usable, Gates: gates}, nil
}

func (f *Fake) listFiles(*http.Request) (any, *failure) {
	out := []tiad.FileInfo{}
	for p, b := range f.files {
		out = append(out, tiad.FileInfo{Path: p, Size: int64(len(b))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return map[string]any{"files": out}, nil
}

func (f *Fake) putFile(r *http.Request) (any, *failure) {
	if fl := contentType(r, "application/octet-stream"); fl != nil {
		return nil, fl
	}
	p, fl := cleanPath(r.PathValue("path"))
	if fl != nil {
		return nil, fl
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "body: %v", err)
	}
	f.files[p] = b
	return tiad.FileInfo{Path: p, Size: int64(len(b))}, nil
}

func (f *Fake) getFile(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, fl := cleanPath(r.PathValue("path"))
	if fl != nil {
		reply(w, nil, fl)
		return
	}
	b, ok := f.files[p]
	if !ok {
		reply(w, nil, fail(http.StatusNotFound, tiad.KindNotFound, "no file %s", p))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(b)
}

func (f *Fake) deleteFile(r *http.Request) (any, *failure) {
	p, fl := cleanPath(r.PathValue("path"))
	if fl != nil {
		return nil, fl
	}
	if _, ok := f.files[p]; !ok {
		return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no file %s", p)
	}
	delete(f.files, p)
	return struct{}{}, nil
}

func (f *Fake) installGSD(r *http.Request) (any, *failure) {
	var req struct {
		Path string `json:"path"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	p, fl := cleanPath(req.Path)
	if fl != nil {
		return nil, fl
	}
	if _, ok := f.files[p]; !ok {
		return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no file %s", p)
	}
	name := path.Base(p)
	if !strings.HasPrefix(strings.ToUpper(name), "GSDML-") || !strings.HasSuffix(strings.ToLower(name), ".xml") {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "%s is not named like a GSDML file (GSDML-*.xml)", name)
	}
	if fl := checkGSDML(name, f.files[p]); fl != nil {
		return nil, fl
	}
	already := f.gsd[name]
	f.gsd[name] = true
	return tiad.GSDInstall{Path: p, AlreadyInstalled: already}, nil
}

// projectDirHolding returns the open session whose project directory holds
// dir: a created project's guard, an opened project's own directory. A
// project at the work dir's root holds none.
func (f *Fake) projectDirHolding(dir string) *session {
	d := strings.TrimSuffix(dir, "/") + "/"
	for _, s := range f.sessions {
		own := s.guard
		if own == "" {
			i := strings.LastIndex(s.project, "/")
			if i < 0 {
				continue
			}
			own = s.project[:i+1]
		}
		if len(d) >= len(own) && strings.EqualFold(d[:len(own)], own) {
			return s
		}
	}
	return nil
}

// checkGSDML is the agent's guard before TIA parses a GSDML file: well-
// formed XML with a root element and no DOCTYPE, so no external entity
// reaches TIA's importer.
func checkGSDML(name string, b []byte) *failure {
	dec := xml.NewDecoder(bytes.NewReader(b))
	root := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(http.StatusBadRequest, tiad.KindBadRequest, "%s is not well-formed XML without a DOCTYPE: tiad never hands TIA a DOCTYPE", name)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			root = true
		case xml.Directive:
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(string(t))), "DOCTYPE") {
				return fail(http.StatusBadRequest, tiad.KindBadRequest, "%s is not well-formed XML without a DOCTYPE: tiad never hands TIA a DOCTYPE", name)
			}
		}
	}
	if !root {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "%s has no root element", name)
	}
	return nil
}

// checkStation applies the agent-side station rules: the spec's own,
// installed GSDMLs, and a password for every web user.
func (f *Fake) checkStation(st tiad.Station, sec tiad.Secrets) *failure {
	if err := st.Validate(); err != nil {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "%v", err)
	}
	if p := st.Profinet; p != nil {
		for _, d := range p.Devices {
			if !f.gsd[d.GSDML] {
				return fail(http.StatusBadRequest, tiad.KindBadRequest, "device %s: GSDML %s is not installed (POST /v1/gsd/install first)", d.Name, d.GSDML)
			}
		}
	}
	if w := st.WebServer; w != nil && w.Enabled {
		for _, u := range w.Users {
			if sec.WebUsers[u.Name] == "" {
				return fail(http.StatusBadRequest, tiad.KindBadRequest, "web user %s has no password in secrets.webUsers", u.Name)
			}
		}
	}
	return nil
}

func (f *Fake) newSession(project string) *session {
	f.next++
	s := &session{id: fmt.Sprintf("s%d", f.next), project: project, changed: true}
	f.sessions[s.id] = s
	return s
}

// projectOpen compares paths case-insensitively: the work dir is on
// Windows, where projects/P/P.ap21 and projects/p/p.ap21 are one file. A
// created project owns its directory (guard): nothing in it opens while
// it is open, and it is not created again while anything in it is open.
func (f *Fake) projectOpen(project, guard string) bool {
	hasPrefix := func(s, prefix string) bool {
		return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
	}
	for _, s := range f.sessions {
		if strings.EqualFold(s.project, project) ||
			guard != "" && hasPrefix(s.project, guard) ||
			s.guard != "" && hasPrefix(project, s.guard) {
			return true
		}
	}
	return false
}

func (f *Fake) createProject(r *http.Request) (any, *failure) {
	var req struct {
		Name    string       `json:"name"`
		Station tiad.Station `json:"station"`
		Secrets tiad.Secrets `json:"secrets"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if err := tiad.CheckProjectName(req.Name); err != nil {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "%v", err)
	}
	if fl := f.checkStation(req.Station, req.Secrets); fl != nil {
		return nil, fl
	}
	project := "projects/" + req.Name + "/" + req.Name + ".ap21"
	guard := "projects/" + req.Name + "/"
	if f.projectOpen(project, guard) {
		return nil, fail(http.StatusConflict, tiad.KindBusy, "project %s, or something in %s, is open", project, guard)
	}
	s := f.newSession(project)
	s.guard = guard
	st := req.Station
	s.station = &st
	f.files[project] = []byte("fake TIA project")
	return tiad.Session{ID: s.id, Project: project}, nil
}

func (f *Fake) openSession(r *http.Request) (any, *failure) {
	var req struct {
		Path string `json:"path"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	p, fl := cleanPath(req.Path)
	if fl != nil {
		return nil, fl
	}
	if _, ok := f.files[p]; !ok {
		return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no project %s", p)
	}
	if f.projectOpen(p, "") {
		return nil, fail(http.StatusConflict, tiad.KindBusy, "project %s is already open", p)
	}
	s := f.newSession(p)
	return tiad.Session{ID: s.id, Project: p}, nil
}

func (f *Fake) listSessions(*http.Request) (any, *failure) {
	out := []tiad.Session{}
	for _, s := range f.sessions {
		out = append(out, tiad.Session{ID: s.id, Project: s.project})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return map[string]any{"sessions": out}, nil
}

func (f *Fake) session(r *http.Request) (*session, *failure) {
	s, ok := f.sessions[r.PathValue("id")]
	if !ok {
		return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no session %s", r.PathValue("id"))
	}
	return s, nil
}

func (f *Fake) closeSession(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	delete(f.sessions, s.id)
	for addr, id := range f.claims {
		if id == s.id {
			delete(f.claims, addr)
		}
	}
	return struct{}{}, nil
}

func (f *Fake) applyStation(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req struct {
		Station tiad.Station `json:"station"`
		Secrets tiad.Secrets `json:"secrets"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if fl := f.checkStation(req.Station, req.Secrets); fl != nil {
		return nil, fl
	}
	changes := StationChanges(s.station, req.Station)
	st := req.Station
	s.station = &st
	s.uploaded = false
	if len(changes) > 0 {
		s.compiled = false
		s.changed = true
	}
	return tiad.StationResult{Changes: changes}, nil
}

// blockHead matches the first line of an SCL block in a source file.
var blockHead = regexp.MustCompile(`^\s*(ORGANIZATION_BLOCK|FUNCTION_BLOCK|FUNCTION|DATA_BLOCK|TYPE)\s+"([^"]+)"`)

var blockKind = map[string]string{
	"ORGANIZATION_BLOCK": "OB", "FUNCTION_BLOCK": "FB", "FUNCTION": "FC", "DATA_BLOCK": "DB", "TYPE": "UDT",
}

func (f *Fake) software(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req tiad.SoftwareRequest
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if req.Group == "" {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "group is empty")
	}
	res := tiad.SoftwareResult{Results: []tiad.SourceResult{}}
	var made []tiad.Block
	for _, src := range append(append([]string{}, req.Sources...), req.TagTables...) {
		p, fl := cleanPath(src)
		if fl != nil {
			return nil, fl
		}
		b, ok := f.files[p]
		if !ok {
			return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no file %s", p)
		}
		sr := tiad.SourceResult{Path: p, OK: true}
		if isTagTable(p, req.TagTables) {
			res.Results = append(res.Results, sr)
			continue
		}
		found := 0
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if m := blockHead.FindStringSubmatch(sc.Text()); m != nil {
				made = append(made, tiad.Block{Name: m[2], Kind: blockKind[m[1]]})
				found++
			}
		}
		if found == 0 {
			sr.OK = false
			sr.Messages = []string{"no block found in source"}
		}
		res.Results = append(res.Results, sr)
	}
	byName := map[string]tiad.Block{}
	if !req.Prune {
		for _, b := range s.blocks {
			byName[strings.ToLower(b.Name)] = b
		}
	}
	for _, b := range made {
		byName[strings.ToLower(b.Name)] = b
	}
	s.blocks = s.blocks[:0]
	for _, b := range byName {
		s.blocks = append(s.blocks, b)
	}
	sort.Slice(s.blocks, func(i, j int) bool { return s.blocks[i].Name < s.blocks[j].Name })
	s.compiled = false
	s.changed = true
	res.Blocks = append([]tiad.Block{}, s.blocks...)
	return res, nil
}

func isTagTable(p string, tables []string) bool {
	for _, t := range tables {
		if c, _ := cleanPath(t); c == p {
			return true
		}
	}
	return false
}

func (f *Fake) compile(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req struct {
		Hardware bool `json:"hardware"`
		Software bool `json:"software"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if !req.Hardware && !req.Software {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "neither hardware nor software selected")
	}
	res := f.CompileResult
	if res.Result == "" {
		res.Result = "success"
	}
	if res.Messages == nil {
		res.Messages = []tiad.CompileMessage{}
	}
	if res.Errors > 0 {
		s.compiled = false
		return nil, &failure{status: http.StatusUnprocessableEntity, kind: tiad.KindCompileFailed,
			msg: fmt.Sprintf("%d errors", res.Errors), data: res}
	}
	// Only a full compile makes the project downloadable.
	if req.Hardware && req.Software {
		s.compiled = true
	}
	return res, nil
}

func (f *Fake) download(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req tiad.DownloadRequest
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if err := req.Validate(); err != nil {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "%v", err)
	}
	if !contains(f.Targets, req.Address) {
		return nil, fail(http.StatusForbidden, tiad.KindTargetRefused, "%s is not in TIAD_TARGETS", req.Address)
	}
	if s.station == nil || s.uploaded {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "the project has no station spec")
	}
	if s.station.CPU.Address != req.Address {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest,
			"the project's CPU is at %s, not %s: one station spec per CPU", s.station.CPU.Address, req.Address)
	}
	if !s.compiled {
		return nil, fail(http.StatusConflict, tiad.KindNotCompiled, "the project changed since the last full compile; compile hardware and software first")
	}
	// One project per CPU: the first download claims the CPU for its
	// session, dry runs included, until that session is closed.
	if holder, ok := f.claims[req.Address]; ok && holder != s.id {
		return nil, fail(http.StatusConflict, tiad.KindBusy, "CPU %s belongs to session %s: one CPU, one project; close that session first", req.Address, holder)
	}
	f.claims[req.Address] = s.id
	var qs []tiad.Question
	if f.PreDownload != nil {
		qs = f.PreDownload(req)
	}
	if qs == nil {
		qs = []tiad.Question{}
	}
	// As tiad.Openness does: whether the CPU starts after loading is
	// always decided before loading.
	startAsked := false
	for _, q := range qs {
		startAsked = startAsked || q.ID == tiad.QuestionStartAfter
	}
	if !startAsked {
		qs = append(append([]tiad.Question{}, qs...), tiad.Question{ID: tiad.QuestionStartAfter, Message: "start the CPU after loading", Detail: "StartModules"})
	}
	// A dry run loading nothing is the contract tiad aims for. Whether
	// Openness can list the checks and cancel with nothing loaded is
	// provisional until S2 (siemens-ide.md §3.5); the fake models the aim.
	if req.DryRun {
		return tiad.DownloadResult{DryRun: true, Provisional: true, Questions: qs}, nil
	}
	haveProtection := req.Secrets != nil && req.Secrets.Protection != ""
	unanswered, declined := tiad.Resolve(qs, req.Decisions, haveProtection)
	if len(unanswered) > 0 {
		return nil, &failure{status: http.StatusConflict, kind: tiad.KindDecisionRequired,
			msg:  fmt.Sprintf("%d question(s) have no answer; nothing was loaded", len(unanswered)),
			data: map[string]any{"questions": unanswered}}
	}
	if len(declined) > 0 {
		return nil, &failure{status: http.StatusConflict, kind: tiad.KindDeclined,
			msg:  fmt.Sprintf("%d question(s) were answered no; nothing was loaded", len(declined)),
			data: map[string]any{"questions": declined}}
	}
	state := "RUN"
	stopped := false
	for _, q := range qs {
		if q.ID == tiad.QuestionStopCPU {
			stopped = true
		}
	}
	if stopped && (req.Decisions.StartAfter == nil || !*req.Decisions.StartAfter) {
		state = "STOP"
	}
	rec := req
	rec.Secrets = nil
	f.downloads = append(f.downloads, Download{Session: s.id, Request: rec, State: state})
	f.loaded[req.Address] = s.id
	s.changed = false
	return tiad.DownloadResult{Downloaded: true, Questions: qs, State: state}, nil
}

func (f *Fake) compare(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	if fl := decode(r, &struct{}{}); fl != nil {
		return nil, fl
	}
	if f.CompareResult != nil {
		return *f.CompareResult, nil
	}
	if s.station == nil {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "the project has no station spec")
	}
	if !contains(f.Targets, s.station.CPU.Address) {
		return nil, fail(http.StatusForbidden, tiad.KindTargetRefused, "%s is not in TIAD_TARGETS", s.station.CPU.Address)
	}
	if f.loaded[s.station.CPU.Address] == s.id && !s.changed {
		return tiad.CompareResult{Equal: true, Differences: []tiad.Difference{}}, nil
	}
	return tiad.CompareResult{Differences: []tiad.Difference{{Object: s.station.CPU.Name, State: "different"}}}, nil
}

func (f *Fake) export(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req tiad.ExportRequest
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	dir, fl := cleanPath(req.Dir)
	if fl != nil {
		return nil, fl
	}
	// An export never writes into an open project's directory.
	if holder := f.projectDirHolding(dir); holder != nil {
		return nil, fail(http.StatusConflict, tiad.KindBusy, "%s is inside the directory of the open project %s", dir, holder.project)
	}
	var out []string
	put := func(name string, b []byte) {
		p := dir + "/" + name
		f.files[p] = b
		out = append(out, p)
	}
	switch req.Blocks {
	case "":
	case "scl", "xml":
		for _, b := range s.blocks {
			put(b.Name+"."+req.Blocks, []byte(fmt.Sprintf("// fake export of %s %q\n", b.Kind, b.Name)))
		}
	default:
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "blocks %q: one of scl, xml or empty", req.Blocks)
	}
	if req.TagTables {
		put("tagtables.xml", []byte("<!-- fake tag tables -->\n"))
	}
	if req.Hardware || req.GSDML {
		if s.station == nil {
			return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "the project has no station spec")
		}
	}
	if req.Hardware {
		b, _ := json.MarshalIndent(s.station, "", "  ")
		put("station.json", b)
	}
	if req.GSDML {
		put("GSDML-V2.4-fake-"+s.station.CPU.Name+".xml", []byte("<!-- fake I-device GSDML -->\n"))
	}
	if out == nil {
		out = []string{}
	}
	return tiad.ExportResult{Files: out}, nil
}

func (f *Fake) upload(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req tiad.UploadRequest
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if s.station == nil || !contains(f.Targets, s.station.CPU.Address) {
		return nil, fail(http.StatusForbidden, tiad.KindTargetRefused, "the project's CPU is not in TIAD_TARGETS")
	}
	s.compiled = false
	s.changed = true
	s.uploaded = true
	// The fake's CPU asks no TLS question, so nothing is trusted.
	return tiad.UploadResult{TrustedBlindly: []string{}}, nil
}

func (f *Fake) save(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req struct {
		Archive bool `json:"archive"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	p := s.project
	if req.Archive {
		p = strings.TrimSuffix(p, ".ap21") + ".zap21"
	}
	f.files[p] = []byte("fake TIA project")
	return map[string]string{"path": p}, nil
}

// StationChanges describes, in words, what applying next over prev
// changes. The real agent reports TIA's own account; the fake's is enough
// to tell "nothing" from "something" and to name added and removed
// devices and modules.
func StationChanges(prev *tiad.Station, next tiad.Station) []string {
	out := []string{}
	if prev == nil {
		return append(out, "station created")
	}
	if prev.CPU != next.CPU {
		out = append(out, "cpu "+next.CPU.Name+" changed")
	}
	if !jsonEqual(prev.WebServer, next.WebServer) {
		out = append(out, "web server changed")
	}
	mods := func(ms []tiad.Module) map[int]tiad.Module {
		m := map[int]tiad.Module{}
		for _, x := range ms {
			m[x.Slot] = x
		}
		return m
	}
	pm, nm := mods(prev.Modules), mods(next.Modules)
	for _, slot := range sortedKeys(pm, nm) {
		a, inA := pm[slot]
		b, inB := nm[slot]
		switch {
		case !inA:
			out = append(out, fmt.Sprintf("module slot %d added", slot))
		case !inB:
			out = append(out, fmt.Sprintf("module slot %d removed", slot))
		case !jsonEqual(a, b):
			out = append(out, fmt.Sprintf("module slot %d changed", slot))
		}
	}
	devs := func(p *tiad.Profinet) map[string]tiad.Device {
		m := map[string]tiad.Device{}
		if p != nil {
			for _, d := range p.Devices {
				m[d.Name] = d
			}
		}
		return m
	}
	pd, nd := devs(prev.Profinet), devs(next.Profinet)
	names := map[string]bool{}
	for n := range pd {
		names[n] = true
	}
	for n := range nd {
		names[n] = true
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		a, inA := pd[n]
		b, inB := nd[n]
		switch {
		case !inA:
			out = append(out, "device "+n+" added")
		case !inB:
			out = append(out, "device "+n+" removed")
		case !jsonEqual(a, b):
			out = append(out, "device "+n+" changed")
		}
	}
	if (prev.Profinet == nil) != (next.Profinet == nil) ||
		prev.Profinet != nil && next.Profinet != nil && prev.Profinet.UpdateTime != next.Profinet.UpdateTime {
		out = append(out, "IO system changed")
	}
	return out
}

func sortedKeys(a, b map[int]tiad.Module) []int {
	seen := map[int]bool{}
	var out []int
	for _, m := range []map[int]tiad.Module{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Ints(out)
	return out
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
