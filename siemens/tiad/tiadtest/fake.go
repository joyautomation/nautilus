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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
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
	// Token, when set, is the bearer token every request must carry.
	Token string
	// Targets are the CPU addresses downloads may go to (TIAD_TARGETS).
	Targets []string
	// Gates are what probe reports. Empty means every gate passes. Any
	// failing gate makes every TIA call fail with kind "gate".
	Gates []tiad.Gate
	// PreDownload scripts TIA's pre-download checks. Nil means none.
	PreDownload func(tiad.DownloadRequest) []tiad.Question
	// CompileResult is what compile returns. Zero means success. A result
	// with errors fails the call with kind "compile_failed".
	CompileResult tiad.CompileResult
	// CompareResult, when set, is what compare returns. Otherwise the
	// project equals the CPU exactly when this session's project is the
	// last one downloaded to the station's CPU.
	CompareResult *tiad.CompareResult

	mu        sync.Mutex
	files     map[string][]byte
	gsd       map[string]bool
	sessions  map[string]*session
	next      int
	downloads []Download
	loaded    map[string]string // CPU address → session that last downloaded
	mux       *http.ServeMux
	once      sync.Once
}

type session struct {
	id, project string
	station     *tiad.Station
	blocks      []tiad.Block
	compiled    bool
}

// Download records one real download. Secrets are never recorded.
type Download struct {
	Session string
	Request tiad.DownloadRequest
	State   string
}

// New returns an empty fake.
func New() *Fake { return &Fake{} }

// Serve starts the fake on a loopback port, closed when the test ends, and
// returns a client for it.
func (f *Fake) Serve(t testing.TB) (*httptest.Server, *tiad.Client) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, tiad.New(srv.URL, f.Token)
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
	if f.Token != "" && r.Header.Get("Authorization") != "Bearer "+f.Token {
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
				if !g.OK {
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

func decode(r *http.Request, v any) *failure {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "body: %v", err)
	}
	return nil
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
			gates = append(gates, tiad.Gate{Name: n, OK: true})
		}
	}
	usable := true
	for _, g := range gates {
		usable = usable && g.OK
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
	already := f.gsd[name]
	f.gsd[name] = true
	return tiad.GSDInstall{Path: p, AlreadyInstalled: already}, nil
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
	s := &session{id: fmt.Sprintf("s%d", f.next), project: project}
	f.sessions[s.id] = s
	return s
}

func (f *Fake) projectOpen(project string) bool {
	for _, s := range f.sessions {
		if s.project == project {
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
	if req.Name == "" || strings.ContainsAny(req.Name, `/\:`) {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "project name %q is empty or has a path separator", req.Name)
	}
	if fl := f.checkStation(req.Station, req.Secrets); fl != nil {
		return nil, fl
	}
	project := "projects/" + req.Name + "/" + req.Name + ".ap21"
	if f.projectOpen(project) {
		return nil, fail(http.StatusConflict, tiad.KindBusy, "project %s is already open", project)
	}
	s := f.newSession(project)
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
	if f.projectOpen(p) {
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
	if len(changes) > 0 {
		s.compiled = false
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
	if s.station == nil {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "the project has no station spec")
	}
	if s.station.CPU.Address != req.Address {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest,
			"the project's CPU is at %s, not %s: one station spec per CPU", s.station.CPU.Address, req.Address)
	}
	if !s.compiled {
		return nil, fail(http.StatusConflict, tiad.KindOperationFailed, "the project changed since the last full compile; compile hardware and software first")
	}
	var qs []tiad.Question
	if f.PreDownload != nil {
		qs = f.PreDownload(req)
	}
	if qs == nil {
		qs = []tiad.Question{}
	}
	if req.DryRun {
		return tiad.DownloadResult{DryRun: true, Questions: qs}, nil
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
	return tiad.DownloadResult{Downloaded: true, Questions: qs, State: state}, nil
}

func (f *Fake) compare(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
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
	if f.loaded[s.station.CPU.Address] == s.id && s.compiled {
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
	if s.station == nil || !contains(f.Targets, s.station.CPU.Address) {
		return nil, fail(http.StatusForbidden, tiad.KindTargetRefused, "the project's CPU is not in TIAD_TARGETS")
	}
	return struct{}{}, nil
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
