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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/station"
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
	// Device is the CPU's identity, as an upload and an online read
	// report it. Zero means BenchDevice.
	Device tiad.DeviceIdentity
	// NoIdentify makes the fake's TIA side one that cannot read a CPU's
	// identity online, as tiad.Openness cannot yet: online calls to a
	// pinned CPU then fail with device_unverified.
	NoIdentify bool

	// policy is the projects' password policy for local users (nil:
	// TIA's default), uploadAccess the access control an upload brings
	// ("": Disabled). Set them with SetAccess.
	policy        *PasswordPolicy
	uploadAccess  string
	accessRecords string

	mu        sync.Mutex
	pins      map[string]tiad.DeviceIdentity // CPU address → device pin
	files     map[string][]byte
	gsd       map[string]bool
	sessions  map[string]*session
	next      int
	downloads []Download
	loaded    map[string]string // CPU address → session that last downloaded
	// saved is what a saved project holds besides its file: its CPU, the
	// devices besides it and its protection, restored when it is opened.
	saved  map[string]savedProject
	claims map[string]string // CPU address → the session that claimed it (one project per CPU)
	mux    *http.ServeMux
	once   sync.Once
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
	// protection is the CPU's configuration protection as the project
	// holds it (one of the Protection* states), protectedWith the
	// password it was protected with.
	protection, protectedWith string
	// cpu is the project's CPU (nil: none), others the devices besides
	// its station, kept the ones the last apply kept as found.
	cpu    *tiad.ProjectCPU
	others []tiad.KeptDevice
	kept   []tiad.KeptDevice
	// accessControl is the CPU's access control (one of the Access*
	// states), access the project's users and roles, made what applies on
	// this session made in them (an upload removes exactly that).
	accessControl string
	access        access
	made          accessMade
	// accessSet is set once an apply on this session asked for
	// fullAccessUser: user-management keep is refused from then on.
	accessSet bool
	// uploadedFrom is the address this session last uploaded from
	// successfully, "" for none (the agent's Session.UploadedFrom): a
	// later apply keeps it, a new upload clears it when it starts.
	uploadedFrom string
	// unclean is set while an apply or upload runs and stays set when it
	// fails part-way: a later apply or save refuses.
	unclean bool
}

// savedProject is the hardware a saved project holds, as TIA keeps it.
type savedProject struct {
	cpu                       *tiad.ProjectCPU
	others                    []tiad.KeptDevice
	protection, protectedWith string
	accessControl             string
	access                    access
}

// UploadedCPUName and UploadedIODevice are what the fake's upload brings
// besides the CPU's identity: tiad-fake's UploadedCpuName and
// UploadedIoDevice.
const UploadedCPUName = "PLC_1"

// UploadedIODevice is the IO device an upload brings beside the CPU's
// station, as the S2a upload brought one.
func UploadedIODevice() tiad.KeptDevice {
	return tiad.KeptDevice{Name: "io-device-up", ProfinetName: "io-device-up", Address: "192.0.2.30"}
}

// sameFirmware is CpuReuse.SameFirmware: exact, a leading "V" aside.
func sameFirmware(a, b string) bool {
	return strings.TrimPrefix(a, "V") == strings.TrimPrefix(b, "V")
}

func uncleanFailure(what string) *failure {
	return fail(http.StatusConflict, tiad.KindUncleanSession,
		"an apply or upload on this session failed part-way, so its project matches no spec and tiad trusts nothing in it; %s is refused: close this session without saving, then open the saved project again or create it anew", what)
}

// decideCPU is CpuReuse.Decide and CpuReuse.Collides: create, reuse, or
// a refusal naming the fix.
func (s *session) decideCPU(st tiad.Station) (reuse bool, fl *failure) {
	if s.unclean {
		return false, uncleanFailure("an apply")
	}
	if s.cpu == nil {
		return false, nil
	}
	pc := *s.cpu
	var diff []string
	if pc.Name != st.CPU.Name {
		diff = append(diff, fmt.Sprintf("name %q, not %q", pc.Name, st.CPU.Name))
	}
	if pc.Order != st.CPU.Order {
		diff = append(diff, fmt.Sprintf("order %q, not %q", pc.Order, st.CPU.Order))
	}
	if !sameFirmware(pc.Firmware, st.CPU.Firmware) {
		diff = append(diff, fmt.Sprintf("firmware %q, not %q", pc.Firmware, st.CPU.Firmware))
	}
	if len(diff) > 0 {
		return false, &failure{status: http.StatusConflict, kind: tiad.KindCPUMismatch,
			msg:  "the project's CPU has " + strings.Join(diff, ", ") + "; tiad never replaces a project's CPU on an apply: to keep it, apply a spec whose cpu name, order and firmware are the project's (in this error's data); to make a new CPU, create the project again (POST /v1/projects)",
			data: map[string]any{"cpu": pc}}
	}
	names := map[string]bool{strings.ToLower(st.CPU.Name): true, strings.ToLower(st.CPU.ProfinetName): true}
	addresses := map[string]bool{st.CPU.Address: true}
	if st.Profinet != nil {
		for _, d := range st.Profinet.Devices {
			names[strings.ToLower(d.Name)] = true
			if d.Address != "" {
				addresses[d.Address] = true
			}
		}
	}
	delete(names, "")
	var bad []string
	for _, k := range s.others {
		if names[strings.ToLower(k.Name)] || (k.ProfinetName != "" && names[strings.ToLower(k.ProfinetName)]) || (k.Address != "" && addresses[k.Address]) {
			bad = append(bad, describeKept(k))
		}
	}
	if len(bad) > 0 {
		return false, &failure{status: http.StatusConflict, kind: tiad.KindCPUMismatch,
			msg:  "the project keeps device(s) " + strings.Join(bad, "; ") + " that share a name or an address with the spec; tiad does not configure PROFINET IO devices yet and will not let two coexist: change the spec, or remove the device in the project",
			data: map[string]any{"cpu": pc, "keptDevices": s.others}}
	}
	return true, nil
}

// keptList is kept as the agent lists it: never null.
func keptList(kept []tiad.KeptDevice) []tiad.KeptDevice {
	if kept == nil {
		return []tiad.KeptDevice{}
	}
	return kept
}

// describeKept is KeptDevice.Describe.
func describeKept(k tiad.KeptDevice) string {
	d := strconv.Quote(k.Name)
	if k.ProfinetName != "" {
		d += " profinetName=" + strconv.Quote(k.ProfinetName)
	}
	if k.Address != "" {
		d += " address=" + strconv.Quote(k.Address)
	}
	return d
}

// keptNote is CpuReuse.KeptNote.
func keptNote(kept []tiad.KeptDevice) string {
	if len(kept) == 0 {
		return ""
	}
	var ds []string
	for _, k := range kept {
		ds = append(ds, describeKept(k))
	}
	return "the project's other devices are kept as found (tiad does not configure PROFINET IO devices yet), and a download loads them too: " + strings.Join(ds, "; ")
}

// The CPU's configuration protection as a project holds it: TIA's
// MasterSecretConfiguration names. ProtectionAbsent is a CPU without one.
const (
	ProtectionNone            = "None"
	ProtectionWithoutPassword = "WithoutPassword"
	ProtectionWithPassword    = "WithPassword"
	ProtectionAbsent          = ""
)

// NotConfigured is how TIA fails a hardware compile of a FW 3.1 CPU whose
// configuration protection is not set up.
const NotConfigured = "Password for confidential PLC configuration data is not configured in the PLC."

const (
	actNothing               = ""
	actProtect               = "protect"
	actProtectThenUnprotect  = "protect-then-unprotect"
	actUnprotectWithPassword = "unprotect-with-password"
)

// planProtection is tiad.Openness's rule (ConfigProtection.Plan in
// tools/tiad/core/TiaRules.cs), with its messages: the action that takes
// the project's state to what want asks, or a refusal naming the fix.
func planProtection(current, want string, havePassword bool) (string, *failure) {
	bad := func(f string, a ...any) (string, *failure) {
		return "", fail(http.StatusBadRequest, tiad.KindBadRequest, f, a...)
	}
	if want == tiad.ProtectKeep {
		if current == ProtectionWithoutPassword {
			return bad("the project's CPU has its configuration protection not set up (TIA's default for a new CPU, which does not compile), so keep has nothing to keep: set cpu.configurationProtection to password or none")
		}
		return actNothing, nil
	}
	if current == ProtectionAbsent {
		return bad("cpu.configurationProtection is %s, and the project's CPU has no configuration protection (firmware 3.1 and later have it): set it to keep", want)
	}
	if current != ProtectionNone && current != ProtectionWithoutPassword && current != ProtectionWithPassword {
		return bad("the project's CPU has its configuration protection in a state tiad does not know (%q); nothing was changed", current)
	}
	if want == tiad.ProtectPassword {
		if !havePassword {
			return bad("cpu.configurationProtection is password, and secrets.configurationPassword is empty (it comes from the secret store, never the spec)")
		}
		if current == ProtectionWithPassword {
			return actNothing, nil
		}
		return actProtect, nil
	}
	switch current {
	case ProtectionNone:
		return actNothing, nil
	case ProtectionWithoutPassword:
		return actProtectThenUnprotect, nil
	}
	if !havePassword {
		return bad("the project's CPU has its configuration protected by a password; turning that off needs it in secrets.configurationPassword")
	}
	return actUnprotectWithPassword, nil
}

// protectionNote is ConfigProtection.Note: password on a CPU already
// protected leaves it, and its password, as they are.
func protectionNote(current, want string) string {
	if current != ProtectionWithPassword || want != tiad.ProtectPassword {
		return ""
	}
	return "the CPU's configuration protection was already on and is left as it is: TIA cannot check its password offline, so a changed secrets.configurationPassword does not reach the project, and a download asks for the password the project holds; to change it, apply none with the old password, then password with the new one"
}

// protect applies cpu.configurationProtection to s's project the way
// TIA's configurator behaves, from the project's state.
func (s *session) protect(want, password string) *failure {
	act, fl := planProtection(s.protection, want, password != "")
	if fl != nil {
		return fl
	}
	tia := func(msg string) *failure {
		return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", msg)
	}
	switch act {
	case actProtect:
		s.protection, s.protectedWith = ProtectionWithPassword, password
	case actUnprotectWithPassword:
		if password != s.protectedWith {
			return tia("The password is not correct.")
		}
		s.protection, s.protectedWith = ProtectionNone, ""
	case actProtectThenUnprotect:
		s.protection = ProtectionNone
	}
	return nil
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
		f.pins = map[string]tiad.DeviceIdentity{}
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
		m.HandleFunc("GET /v1/pins", f.wrap(f.listPins, false))
		m.HandleFunc("POST /v1/pins/{address}", f.wrap(f.setPin, false))
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
	if err := sec.CheckFor(st); err != nil {
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
	// The directory is replaced: nothing saved in it survives.
	for p := range f.saved {
		if strings.HasPrefix(strings.ToLower(p), strings.ToLower(guard)) {
			delete(f.saved, p)
		}
	}
	// As tiad does: the new project's policy is read once it is made, so
	// a refusal comes after the old project of that name was replaced.
	if fl := f.checkPolicy(req.Station, req.Secrets); fl != nil {
		delete(f.sessions, s.id)
		return nil, fl
	}
	// TIA makes a new CPU with its protection not set up and access
	// control on.
	s.protection = ProtectionWithoutPassword
	s.accessControl = AccessEnabled
	s.cpu = &tiad.ProjectCPU{Name: req.Station.CPU.Name, Order: req.Station.CPU.Order, Firmware: req.Station.CPU.Firmware}
	if fl := s.protect(req.Station.CPU.ConfigurationProtection, req.Secrets.ConfigurationPassword); fl != nil {
		delete(f.sessions, s.id)
		return nil, fl
	}
	var notes []string
	if fl := s.applyAccess(req.Station.CPU.AccessControl, req.Secrets.FullAccessPassword, true, &notes); fl != nil {
		delete(f.sessions, s.id)
		return nil, fl
	}
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
	// The project as it was saved: its CPU and its devices.
	if sp, ok := f.saved[p]; ok {
		s.cpu, s.others, s.protection, s.protectedWith = sp.cpu, sp.others, sp.protection, sp.protectedWith
		s.accessControl, s.access = sp.accessControl, sp.access.clone()
		f.openRecords(s.access)
	}
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
		Station              tiad.Station `json:"station"`
		Secrets              tiad.Secrets `json:"secrets"`
		FullAccessRecordOnly bool         `json:"fullAccessRecordOnly"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if fl := f.checkStation(req.Station, req.Secrets); fl != nil {
		return nil, fl
	}
	if req.FullAccessRecordOnly && req.Station.CPU.AccessControl != tiad.AccessFullAccessUser {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "fullAccessRecordOnly goes with cpu.accessControl fullAccessUser")
	}
	// As the agent does: decided from the project's CPU before any write,
	// so a refusal leaves the session as it was.
	reuse, fl := s.decideCPU(req.Station)
	if fl != nil {
		return nil, fl
	}
	var notes []string
	var kept []tiad.KeptDevice
	if reuse {
		if _, fl := planProtection(s.protection, req.Station.CPU.ConfigurationProtection, req.Secrets.ConfigurationPassword != ""); fl != nil {
			return nil, fl
		}
		current := s.accessControl
		if fl := planAccess(&current, req.Station.CPU.AccessControl, req.Secrets.FullAccessPassword != ""); fl != nil {
			return nil, fl
		}
		kept = s.others
	}
	if fl := f.checkPolicy(req.Station, req.Secrets); fl != nil {
		return nil, fl
	}
	// A found user's password and a user or role tiad did not make,
	// before any write: a refusal leaves the session as it was.
	wrote, fl := s.prepareAccess(req.Station.CPU.AccessControl, req.Secrets.FullAccessPassword, req.FullAccessRecordOnly, &notes)
	if fl != nil {
		return nil, fl
	}
	prev := s.station
	if s.uploaded {
		prev = nil
	}
	if !reuse {
		// TIA makes a new CPU with its protection not set up.
		s.cpu = &tiad.ProjectCPU{Name: req.Station.CPU.Name, Order: req.Station.CPU.Order, Firmware: req.Station.CPU.Firmware}
		s.others = nil
		s.protection, s.protectedWith = ProtectionWithoutPassword, ""
		s.accessControl = AccessEnabled
		s.access.dropGrants()
	}
	// Until the spec is applied the project matches no spec: a failure
	// part-way leaves nothing downloadable, and the session unclean.
	// A password set or recorded is a change to the project.
	was, wasChanged := s.compiled && !wrote, s.changed || wrote
	s.station, s.uploaded, s.compiled, s.changed = nil, false, false, true
	s.unclean = true
	if n := protectionNote(s.protection, req.Station.CPU.ConfigurationProtection); n != "" {
		notes = append(notes, n)
	}
	if fl := s.protect(req.Station.CPU.ConfigurationProtection, req.Secrets.ConfigurationPassword); fl != nil {
		return nil, fl
	}
	if fl := s.applyAccess(req.Station.CPU.AccessControl, req.Secrets.FullAccessPassword, !reuse, &notes); fl != nil {
		return nil, fl
	}
	s.unclean = false
	if n := keptNote(kept); n != "" {
		notes = append(notes, n)
	}
	var changes []string
	switch {
	case !reuse:
		changes = StationChanges(nil, req.Station)
	case prev != nil:
		changes = StationChanges(prev, req.Station)
	default:
		changes = []string{"cpu " + req.Station.CPU.Name + " taken over from the project"}
	}
	st := req.Station
	s.station = &st
	s.kept = kept
	if len(changes) == 0 {
		s.compiled, s.changed = was, wasChanged
	}
	return tiad.StationResult{Changes: changes, Notes: notes, KeptDevices: kept}, nil
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
	if req.Hardware && s.protection == ProtectionWithoutPassword {
		res = tiad.CompileResult{Result: "error", Errors: 1, Messages: []tiad.CompileMessage{{Severity: "error", Text: NotConfigured}}}
	} else if req.Hardware && s.accessControl == AccessEnabled && !s.access.fullAccess() {
		res = tiad.CompileResult{Result: "error", Errors: 1, Messages: []tiad.CompileMessage{{Severity: "error", Text: NoFullAccess}}}
	}
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
	call := "download"
	if req.DryRun {
		call = "dry run"
	}
	// The hardware configuration loads unverified (#789 hardware gate):
	// the opt-in and the operator's attestation, on every CPU, before
	// anything reaches the CPU; a dry run lists the question instead.
	var attested *tiad.AttestationRecord
	if !req.DryRun {
		if why, declined := tiad.CheckHardware(req); why != "" {
			kind := tiad.KindDecisionRequired
			if declined {
				kind = tiad.KindDeclined
			}
			return nil, &failure{status: http.StatusConflict, kind: kind,
				msg:  why + "; nothing was sent to the CPU",
				data: map[string]any{"questions": []tiad.Question{tiad.HardwareQuestion()}, "keptDevices": keptList(s.kept)}}
		}
		if req.Hardware {
			attested = &tiad.AttestationRecord{By: req.Attestation.By, Text: req.Attestation.Text, CPUAccessControl: req.Attestation.CPUAccessControl,
				At: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
		}
	}
	// The kept devices load too: a yes naming exactly them, before
	// anything reaches the CPU; a dry run lists the question instead.
	if why := tiad.CheckKept(s.kept, req.Decisions.KeptDevices); why != "" && !req.DryRun {
		return nil, &failure{status: http.StatusConflict, kind: tiad.KindDecisionRequired,
			msg:  why + "; nothing was sent to the CPU",
			data: map[string]any{"questions": []tiad.Question{tiad.KeptQuestion(s.kept)}, "keptDevices": keptList(s.kept)}}
	}
	// A user-management reset acts on the CPU's users and access control:
	// checked before anything reaches the CPU, and the same verdict
	// answers TIA's check below. A dry run never answers.
	accessControlKept := s.station != nil && s.station.CPU.AccessControl == tiad.AccessKeep && !s.accessSet
	var reset tiad.ResetGate
	if !req.DryRun && req.Decisions.UserManagementResetToProject != nil && *req.Decisions.UserManagementResetToProject {
		reset = tiad.CheckUserManagementReset(req, accessControlKept, s.uploadedFrom, tiad.UploadRouteEnabled, func() (string, bool) { return s.accessControl, true })
		if !reset.Allowed {
			return nil, &failure{status: http.StatusConflict, kind: tiad.KindDecisionRequired,
				msg:  reset.Why + "; nothing was sent to the CPU",
				data: map[string]any{"questions": []tiad.Question{tiad.UserManagementResetQuestion(reset.Why)}, "keptDevices": keptList(s.kept)}}
		}
	}
	// Whether the CPU starts after loading is decided before TIA is
	// called: TIA asks it, if at all, only after loading. A dry run never
	// loads, so it needs no answer.
	if !req.DryRun && req.Decisions.StartAfter == nil {
		return nil, &failure{status: http.StatusConflict, kind: tiad.KindDecisionRequired,
			msg: "the request does not say whether the CPU starts after loading (decisions.startAfter); nothing was sent to the CPU",
			data: map[string]any{"questions": []tiad.Question{{ID: tiad.QuestionStartAfter, Message: "start the CPU after loading", Detail: "StartModules"}},
				"keptDevices": keptList(s.kept)}}
	}
	if fl := f.checkDevicePin(req.Address, call); fl != nil {
		return nil, fl
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
	// A protected configuration asks for its password when the hardware
	// loads (provisional until S2b).
	if req.Hardware && s.protection == ProtectionWithPassword {
		qs = append(append([]tiad.Question{}, qs...), tiad.Question{ID: tiad.QuestionConfigurationPassword,
			Message: "password for the confidential PLC configuration data", Detail: "PlcMasterSecretPassword"})
	}
	// As tiad.Openness does (#789 S2b): TIA asks its checks one at a
	// time, and each is answered on its own, only from the request. The
	// first not answered yes cancels there; a dry run cancels at the
	// first. A check tiad does not know is other.
	haveProtection := req.Secrets != nil && req.Secrets.Protection != ""
	haveConfigurationPassword := req.Secrets != nil && req.Secrets.ConfigurationPassword != ""
	var asked []tiad.Question
	var acting []string
	given := ""
	for _, q := range qs {
		if !knownQuestion(q.ID) {
			q.ID = tiad.QuestionOther
		}
		asked = append(asked, q)
		if req.DryRun {
			dq := append([]tiad.Question{}, asked...)
			if req.Hardware {
				dq = append(dq, tiad.HardwareQuestion())
			}
			if len(s.kept) > 0 {
				dq = append(dq, tiad.KeptQuestion(s.kept))
			}
			return tiad.DownloadResult{DryRun: true, Provisional: true, QuestionsIncomplete: true, Questions: dq, KeptDevices: keptList(s.kept)}, nil
		}
		unanswered, declined := tiad.Resolve([]tiad.Question{q}, req.Decisions, haveProtection, haveConfigurationPassword, accessControlKept, reset)
		if len(unanswered) == 0 && len(declined) == 0 {
			// Acting by what the answer writes: start-after no and
			// user-management keep change nothing.
			yes, choice := true, ""
			switch q.ID {
			case tiad.QuestionStartAfter:
				yes = req.Decisions.StartAfter != nil && *req.Decisions.StartAfter
			case tiad.QuestionUserManagement:
				choice = tiad.UserManagementChoice(req.Decisions)
				if choice == tiad.AnswerReset {
					given = reset.Route
				}
			}
			if tiad.ActingAnswer(q.ID, yes, choice) {
				acting = append(acting, q.ID)
			}
			continue
		}
		// A cancel after an acting answer is never a clean refusal: TIA
		// may have acted on it before it asked this check.
		if len(acting) > 0 {
			return nil, outcomeUnknown("tiad cancelled the download at TIA's "+q.ID+" check, which the request does not answer yes", acting)
		}
		if len(unanswered) > 0 {
			return nil, &failure{status: http.StatusConflict, kind: tiad.KindDecisionRequired,
				msg:  "TIA asked a check the request does not answer; nothing was loaded (TIA asks one check at a time: answer it, and it may ask another)",
				data: map[string]any{"questions": unanswered, "keptDevices": keptList(s.kept)}}
		}
		return nil, &failure{status: http.StatusConflict, kind: tiad.KindDeclined,
			msg:  "TIA asked a check the request answers no; nothing was loaded",
			data: map[string]any{"questions": declined, "keptDevices": keptList(s.kept)}}
	}
	if asked == nil {
		asked = []tiad.Question{}
	}
	state := "RUN"
	stopped := false
	for _, q := range asked {
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
	// tiad reports the mode only as TIA's messages say it: TIA says
	// nothing of a CPU it never stopped.
	said, source := "", ""
	if stopped {
		said, source = state, tiad.StateFromTIAMessage
	}
	f.loaded[req.Address] = s.id
	s.changed = false
	// A dry run TIA asks nothing about cannot be cancelled: TIA loads.
	if req.DryRun {
		return nil, outcomeUnknown("TIA ran the dry run's download without asking a single check, so tiad could not cancel it", nil)
	}
	// Success only on evidence: an online compare right after, in the
	// same call, must be equal (it covers the PLC software only). The
	// compare failing, TIA's result state, and start-after named after a
	// load that ends in doubt are modelled by the C# fake only (process
	// tests, knobs downloadState, compareDiffers and compareTree).
	if f.CompareResult != nil && !f.CompareResult.Equal {
		var names []string
		for _, d := range f.CompareResult.Differences {
			names = append(names, strings.TrimSpace(d.Object+" "+d.State))
		}
		fl := outcomeUnknown("TIA reported the download loaded, but the compare right after it differs ("+strings.Join(names, ", ")+")", acting)
		// The mode TIA's messages said, as the agent carries it.
		if said != "" {
			d, _ := fl.data.(map[string]any)
			if d == nil {
				d = map[string]any{}
			}
			d["state"], d["stateSource"] = said, source
			fl.data = d
		}
		return nil, fl
	}
	res := tiad.DownloadResult{Downloaded: true, Questions: asked, State: said, StateSource: source, KeptDevices: keptList(s.kept), CompareEqual: true}
	if req.Hardware {
		res.HardwareVerified = new(bool)
		res.Warning = "the hardware configuration is not verified: Openness has no online hardware compare, so only the PLC software was compared after loading"
		res.Attestation = attested
	}
	res.UserManagementReset = given
	return res, nil
}

// knownQuestion is whether id is a pre-download check tiad maps from a
// TIA configuration type (siemens-ide.md §3.5).
func knownQuestion(id string) bool {
	switch id {
	case tiad.QuestionStopCPU, tiad.QuestionReinitialize, tiad.QuestionOverwrite, tiad.QuestionConsistentDownload, tiad.QuestionStartAfter,
		tiad.QuestionAlarmTexts, tiad.QuestionAllBlocks, tiad.QuestionUserManagement, tiad.QuestionIdentificationData,
		tiad.QuestionPassword, tiad.QuestionConfigurationPassword, tiad.QuestionCertificate:
		return true
	}
	return false
}

// outcomeUnknown is the agent's outcome_unknown for a download, naming
// the acting answers given before it ended and what they may have done.
func outcomeUnknown(why string, acting []string) *failure {
	msg := why
	var data any
	if len(acting) > 0 {
		// In the agent's order: stop, start, load, users, other.
		var stop, start, load, users, other bool
		for _, id := range acting {
			switch id {
			case tiad.QuestionStopCPU, tiad.QuestionReinitialize:
				stop = true
			case tiad.QuestionStartAfter:
				start = true
			case tiad.QuestionConsistentDownload, tiad.QuestionAlarmTexts, tiad.QuestionAllBlocks:
				load = true
			case tiad.QuestionUserManagement:
				users = true
			default:
				other = true
			}
		}
		var l []string
		if stop {
			l = append(l, "TIA may already have stopped the CPU: check its mode and start it")
		}
		if start {
			l = append(l, "TIA may already have started the CPU: check its mode")
		}
		if load {
			l = append(l, "TIA may already have loaded the software: compare")
		}
		if users {
			l = append(l, "TIA may already have reset the CPU's user management to the project's: check its users and access control")
		}
		if other {
			l = append(l, "TIA may already have acted on them: check the CPU's mode and compare")
		}
		msg += "; tiad had answered " + strings.Join(acting, ", ") + " before the download ended, so " + strings.Join(l, "; ")
		data = map[string]any{"actingAnswers": acting}
	}
	return &failure{status: http.StatusInternalServerError, kind: tiad.KindOutcomeUnknown,
		msg: msg + "; the CPU may or may not be loaded: compare before anything else", data: data}
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
	if fl := f.checkDevicePin(s.station.CPU.Address, "compare"); fl != nil {
		return nil, fl
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
	s.uploadedFrom = ""
	s.kept = nil
	// The bench CPU's setting, as the S2a upload found it, its name, and
	// its IO device, which the CPU's station does not hold.
	s.protection, s.protectedWith = ProtectionNone, ""
	s.accessControl = f.uploadAccessControl()
	s.access.dropGrants()
	// What this session made in the users and roles belongs to the
	// station the upload replaces.
	removed := s.removeMade()
	// Unclean until the CPU is known to be the pinned one.
	s.unclean = true
	up := f.device()
	s.cpu = &tiad.ProjectCPU{Name: UploadedCPUName, Order: up.Order, Firmware: up.Firmware}
	s.others = []tiad.KeptDevice{UploadedIODevice()}
	// Trust on first use: the first upload pins the CPU's identity; every
	// later one must match it.
	read := f.device()
	address := s.station.CPU.Address
	pin := tiad.PinFirstTrust
	pinned, ok := f.pins[address]
	if err := read.Validate(); err != nil {
		what := "nothing was pinned"
		if ok {
			what = "the device pin was not checked"
		}
		return nil, fail(http.StatusConflict, tiad.KindDeviceUnverified,
			"the upload from %s went through, but the CPU's identity could not be read from it (%v), so %s; do not download from this session", address, err, what)
	}
	if !ok {
		f.pins[address] = read
	} else if diff := tiad.DeviceMismatch(pinned, read); diff != "" {
		return nil, deviceChanged(address, pinned, read, diff,
			"The pin stands; the session's project now holds that CPU's program: do not download from it.")
	} else {
		pin = tiad.PinMatched
	}
	s.unclean = false
	// The project's CPU is now what this CPU reported.
	s.uploadedFrom = address
	// The fake's CPU asks no TLS question, so nothing is trusted.
	return tiad.UploadResult{TrustedBlindly: []string{}, Device: read, Pin: pin, CPU: *s.cpu, RemovedAccess: removed}, nil
}

// BenchDevice is the fake CPU's identity unless a test says otherwise.
func BenchDevice() tiad.DeviceIdentity {
	return tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1"}
}

func (f *Fake) device() tiad.DeviceIdentity {
	if f.Device == (tiad.DeviceIdentity{}) {
		return BenchDevice()
	}
	return f.Device
}

// SetDevice scripts the CPU's identity and whether it can be read online.
func (f *Fake) SetDevice(d tiad.DeviceIdentity, noIdentify bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Device = d
	f.NoIdentify = noIdentify
}

func describe(d tiad.DeviceIdentity) string {
	show := func(v *string) string {
		if v == nil {
			return "not read"
		}
		return strconv.Quote(*v)
	}
	return "order " + show(&d.Order) + ", firmware " + show(&d.Firmware) + ", serial " + show(d.Serial) + ", mac " + show(d.MAC)
}

func deviceChanged(address string, pinned, now tiad.DeviceIdentity, diff, what string) *failure {
	return fail(http.StatusConflict, tiad.KindDeviceChanged,
		"device changed at %s: %s (pinned: %s; now: %s). %s If the CPU was replaced or updated on purpose, re-pin it: POST /v1/pins/%s with replace: true (audited)",
		address, diff, describe(pinned), describe(now), what, address)
}

// checkDevicePin is the agent's CheckDevicePin: with a device pin, the
// CPU's identity read online must match it.
func (f *Fake) checkDevicePin(address, call string) *failure {
	pinned, ok := f.pins[address]
	if !ok {
		return nil
	}
	if f.NoIdentify {
		return fail(http.StatusConflict, tiad.KindDeviceUnverified,
			"%s has a device pin, but its identity could not be read (this agent's TIA side cannot read a CPU's identity online yet), so the pin cannot be checked; the %s was refused before it began; it lifts only once tiad can read a CPU's identity online (#789): a re-pin (POST /v1/pins/%s, audited) records a new identity but cannot lift this", address, call, address)
	}
	now := f.device()
	if diff := tiad.DeviceMismatch(pinned, now); diff != "" {
		return deviceChanged(address, pinned, now, diff, "The "+call+" was refused before it began.")
	}
	return nil
}

func (f *Fake) listPins(*http.Request) (any, *failure) {
	out := []tiad.Pin{}
	for _, a := range sortedAddrs(f.pins) {
		d := f.pins[a]
		out = append(out, tiad.Pin{Address: a, Device: d, Holds: d.Holds()})
	}
	return map[string]any{"pins": out}, nil
}

func sortedAddrs(m map[string]tiad.DeviceIdentity) []string {
	var out []string
	for a := range m {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func (f *Fake) setPin(r *http.Request) (any, *failure) {
	address := r.PathValue("address")
	if !station.IsIPv4(address) {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "pin: address %s is not an IPv4 address", strconv.Quote(address))
	}
	if !contains(f.Targets, address) {
		return nil, fail(http.StatusForbidden, tiad.KindTargetRefused, "%s is not in TIAD_TARGETS", address)
	}
	var req tiad.PinRequest
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if err := req.Validate(); err != nil {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "%v", err)
	}
	prev, ok := f.pins[address]
	if ok && !req.Replace {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "pin: %s is pinned already (%s); replace it with replace: true", address, describe(prev))
	}
	if !ok && req.Replace {
		return nil, fail(http.StatusNotFound, tiad.KindNotFound, "pin: %s has no device pin to replace", address)
	}
	f.pins[address] = *req.Device
	p := tiad.Pin{Address: address, Device: *req.Device, Holds: req.Device.Holds()}
	if ok {
		p.Previous = &prev
	}
	return p, nil
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
	if s.unclean {
		return nil, uncleanFailure("saving it")
	}
	p := s.project
	if req.Archive {
		p = strings.TrimSuffix(p, ".ap21") + ".zap21"
	}
	f.files[p] = []byte("fake TIA project")
	if f.saved == nil {
		f.saved = map[string]savedProject{}
	}
	var cpu *tiad.ProjectCPU
	if s.cpu != nil {
		c := *s.cpu
		cpu = &c
	}
	f.saved[p] = savedProject{cpu: cpu, others: append([]tiad.KeptDevice(nil), s.others...), protection: s.protection, protectedWith: s.protectedWith,
		accessControl: s.accessControl, access: s.access.clone()}
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
