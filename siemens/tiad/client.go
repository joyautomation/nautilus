// Package tiad is the Go client for tiad, nautilus's agent on a machine
// with TIA Portal: it runs TIA Portal Openness without a user interface and
// speaks JSON over HTTP (docs/design/siemens-ide.md §3).
//
// The agent is to Siemens what logixd is to Logix, and the envelope is the
// same: every reply is {ok, data, events}, a failure carries
// {kind, message, fatal}, and the events ride along on both. Two things
// differ on purpose:
//
//   - The hardware side is declarative. A caller sends a Station (the whole
//     hardware config of one CPU) and tiad makes the project match it; no
//     caller ever mirrors Openness method names.
//   - A download never decides anything on its own. A dry run returns TIA's
//     pre-download questions; a real download answers each one only from
//     the request's Decisions, and refuses when one is unanswered (see
//     Resolve). Nothing defaults to "yes, stop the CPU".
//
// This package imports nothing else from nautilus: it is the Siemens
// target's anti-corruption layer towards the agent, and the fake agent in
// tiadtest is built on the same types and rules.
package tiad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// DefaultURL is where a loopback agent listens.
const DefaultURL = "http://127.0.0.1:8190"

// TokenFile is where the agent's installer writes its bearer token, on the
// agent's machine. Copy it to the client's machine and point
// NAUTILUS_TIAD_TOKEN_FILE at the copy, or put its contents in
// NAUTILUS_TIAD_TOKEN. A token is never a command-line flag, because
// argv is visible to every user on the machine.
const TokenFile = `%ProgramData%\tiad\tiad.token`

// Error kinds the agent answers with (siemens-ide.md §3.2). Two more come
// from this client: KindUnreachable and KindBadReply.
const (
	KindBadRequest       = "bad_request"
	KindUnauthorized     = "unauthorized"
	KindNotFound         = "not_found"
	KindBusy             = "busy"
	KindGate             = "gate"
	KindTargetRefused    = "target_refused"
	KindDecisionRequired = "decision_required"
	KindDeclined         = "declined"
	KindCompileFailed    = "compile_failed"
	KindOperationFailed  = "operation_failed"
	// KindNotCompiled refuses a download of a project changed since its
	// last full compile; nothing reached TIA.
	KindNotCompiled = "not_compiled"
	// KindOutcomeUnknown: a download may have loaded the CPU, or not
	// (TIA failed once the download was decided, or TIA's side broke the
	// contract). The session stays open; compare on it before anything
	// else. Unless TIA Portal itself died in the download: then the error
	// is fatal and names the fix (restart the agent, then compare in a
	// fresh session).
	KindOutcomeUnknown = "outcome_unknown"
	// KindDeviceChanged refuses an online call to a CPU whose identity
	// differs from its device pin (§3.8). Re-pin only after a deliberate
	// replacement or firmware update.
	KindDeviceChanged = "device_changed"
	// KindDeviceUnverified refuses an online call to a pinned CPU whose
	// identity could not be read, so the pin could not be checked.
	KindDeviceUnverified = "device_unverified"
	// KindCPUMismatch refuses an apply whose spec does not fit the
	// project's CPU (name, order or firmware), or whose devices collide
	// with ones the project keeps; ProjectCPUOf reads the project's CPU
	// from it. tiad never replaces a project's CPU on an apply: create the
	// project again for a new one.
	KindCPUMismatch = "cpu_mismatch"
	// KindUncleanSession refuses an apply or a save after an apply or an
	// upload failed part-way on the session: close it without saving.
	KindUncleanSession = "unclean_session"

	KindUnreachable = "unreachable"
	KindBadReply    = "bad_reply"
)

// Client talks to one tiad agent.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a client. An empty baseURL falls back to NAUTILUS_TIAD_URL and
// then to DefaultURL; an empty token to NAUTILUS_TIAD_TOKEN, then to the
// contents of the file NAUTILUS_TIAD_TOKEN_FILE names.
func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("NAUTILUS_TIAD_URL")
	}
	if baseURL == "" {
		baseURL = DefaultURL
	}
	if token == "" {
		token = os.Getenv("NAUTILUS_TIAD_TOKEN")
	}
	if token == "" {
		if f := os.Getenv("NAUTILUS_TIAD_TOKEN_FILE"); f != "" {
			if b, err := os.ReadFile(f); err == nil {
				token = strings.TrimSpace(string(b))
			}
		}
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		// Opening a project, compiling and downloading are minutes long.
		// Callers bound them with a context.
		HTTP: &http.Client{Timeout: 30 * time.Minute},
	}
}

// Event is one entry from TIA's own message stream for a call.
type Event struct {
	Kind    string `json:"kind"` // "info" | "warning" | "error"
	Source  string `json:"source"`
	Message string `json:"message"`
}

func (e Event) String() string {
	if e.Source == "" {
		return fmt.Sprintf("%s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("%s [%s]: %s", e.Kind, e.Source, e.Message)
}

// Error is a failure reported by the agent, or by this client when the
// agent could not be reached or answered something that is not an
// envelope.
type Error struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// Fatal means the session is gone (or the agent is): reopen rather
	// than retry.
	Fatal  bool            `json:"fatal"`
	Status int             `json:"-"`
	Events []Event         `json:"-"`
	Data   json.RawMessage `json:"-"`
	// Err is the underlying cause of an unreachable error, such as the
	// context's own error, so errors.Is(err, context.Canceled) works.
	Err error `json:"-"`
}

func (e *Error) Error() string { return fmt.Sprintf("tiad: %s: %s", e.Kind, e.Message) }

func (e *Error) Unwrap() error { return e.Err }

// transportError is the Error for a request that got no reply. A call that
// changes the CPU (Download) and ends this way has an UNKNOWN outcome: the
// agent may have finished it. Compare before retrying.
func (c *Client) transportError(ctx context.Context, err error) *Error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return &Error{Kind: KindUnreachable, Message: fmt.Sprintf("%s: %v", c.BaseURL, err), Fatal: true, Err: err}
}

// Kind returns a tiad error's kind, or "" for any other error.
func Kind(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// OutcomeUnknown reports whether err leaves a CPU's state in doubt: the
// agent said outcome_unknown, or never answered (unreachable). After a
// download, either means compare before anything else; never retry
// blindly.
func OutcomeUnknown(err error) bool {
	k := Kind(err)
	return k == KindOutcomeUnknown || k == KindUnreachable
}

// IsFatal reports whether err is a tiad error that ended the session.
func IsFatal(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Fatal
}

// Questions returns the unanswered or declined download questions carried
// by a decision_required or declined error.
func Questions(err error) []Question {
	var e *Error
	if !errors.As(err, &e) || len(e.Data) == 0 {
		return nil
	}
	var d struct {
		Questions []Question `json:"questions"`
	}
	if json.Unmarshal(e.Data, &d) != nil {
		return nil
	}
	return d.Questions
}

// ProjectCPUOf returns the project's CPU carried by a cpu_mismatch
// error: the name, order and firmware a spec must name to reuse it.
func ProjectCPUOf(err error) (ProjectCPU, bool) {
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindCPUMismatch || len(e.Data) == 0 {
		return ProjectCPU{}, false
	}
	var d struct {
		CPU *ProjectCPU `json:"cpu"`
	}
	if json.Unmarshal(e.Data, &d) != nil || d.CPU == nil {
		return ProjectCPU{}, false
	}
	return *d.CPU, true
}

// CompileErrors returns the compile result carried by a compile_failed
// error.
func CompileErrors(err error) (CompileResult, bool) {
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindCompileFailed || len(e.Data) == 0 {
		return CompileResult{}, false
	}
	var r CompileResult
	if json.Unmarshal(e.Data, &r) != nil {
		return CompileResult{}, false
	}
	return r, true
}

// errorWire is Error as it travels; Error itself hides Data and Status.
type errorWire struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("tiad: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, c.unauthorized()
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	var env struct {
		OK     bool            `json:"ok"`
		Data   json.RawMessage `json:"data"`
		Error  *errorWire      `json:"error"`
		Events []Event         `json:"events"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &Error{Kind: KindBadReply, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 400)), Status: resp.StatusCode, Fatal: true}
	}
	if !env.OK || env.Error != nil {
		e := &Error{Kind: "unknown", Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		if env.Error != nil {
			e.Kind, e.Message, e.Fatal = env.Error.Kind, env.Error.Message, env.Error.Fatal
		}
		e.Status, e.Events, e.Data = resp.StatusCode, env.Events, env.Data
		return env.Events, e
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return env.Events, fmt.Errorf("tiad: decoding %s reply: %w", path, err)
		}
	}
	return env.Events, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) ([]Event, error) {
	if body == nil {
		return c.do(ctx, method, path, nil, "", out)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("tiad: encoding request: %w", err)
	}
	return c.do(ctx, method, path, bytes.NewReader(raw), "application/json", out)
}

// unauthorized is the agent's 401 in words: which agent refused, and where
// the token it wants comes from.
func (c *Client) unauthorized() *Error {
	msg := fmt.Sprintf("%s refused the token; check NAUTILUS_TIAD_TOKEN or NAUTILUS_TIAD_TOKEN_FILE against %s on the agent's machine", c.BaseURL, TokenFile)
	if c.Token == "" {
		msg = fmt.Sprintf("%s wants a token and none was sent; set NAUTILUS_TIAD_TOKEN, or NAUTILUS_TIAD_TOKEN_FILE to a copy of %s from the agent's machine", c.BaseURL, TokenFile)
	}
	return &Error{Kind: KindUnauthorized, Message: msg, Status: http.StatusUnauthorized, Fatal: true}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- health and gates ------------------------------------------------------

// Health is the agent's self-report.
type Health struct {
	Version  string   `json:"version"`
	TIA      string   `json:"tia"`      // installed TIA Portal major, e.g. "V21"
	Openness string   `json:"openness"` // Openness API version in use
	Sessions int      `json:"sessions"`
	Targets  []string `json:"targets"` // CPU addresses tiad may go online to
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	_, err := c.doJSON(ctx, http.MethodGet, "/v1/health", nil, &h)
	return h, err
}

// Gate states (siemens-ide.md §3.4).
const (
	// GateOK: checked, and it holds.
	GateOK = "ok"
	// GateFailed: checked, and it does not. Every TIA call fails with
	// KindGate.
	GateFailed = "failed"
	// GateUnknown: not readable yet, such as the licence before TIA's
	// first start. Reported, but it blocks nothing.
	GateUnknown = "unknown"
)

// Gate is one prerequisite check: "tia", "openness-group",
// "openness-allowlist", "licence", "workdir". OK is true only in state
// GateOK: an unknown gate is not OK, and does not block either.
type Gate struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

// Blocks reports whether the gate stops TIA calls. Every state but
// GateOK and GateUnknown blocks, so a state this client does not know,
// or none, fails closed.
func (g Gate) Blocks() bool { return g.State != GateOK && g.State != GateUnknown }

// Probe is the agent's answer to "can TIA be driven right now". Usable
// means no gate blocks; a gate may still be unknown.
type Probe struct {
	Usable bool   `json:"usable"`
	Gates  []Gate `json:"gates"`
}

// Probe checks every gate. A probe that answers "not usable" is a
// successful probe: err is nil and Usable is false.
func (c *Client) Probe(ctx context.Context) (Probe, error) {
	var p Probe
	_, err := c.doJSON(ctx, http.MethodGet, "/v1/probe", nil, &p)
	return p, err
}

// --- files -------------------------------------------------------------------

// FileInfo is one file in the agent's work dir. Paths are always relative
// to the work dir, with forward slashes.
type FileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// CleanPath applies the work-dir sandbox rule every path in a request
// obeys, and returns the cleaned path. The agent's work dir is on Windows,
// so the rule is Windows-aware: relative, forward slashes, no "..", no
// drive letters or other ':' (alternate data streams), no control
// characters or <>"|?*, no segment ending in a dot or a space, and no
// reserved device name (CON, PRN, AUX, NUL, COM0-9, LPT0-9, COM¹²³,
// LPT¹²³, with or without an extension). The client checks every path it sends, and the
// agent checks again.
func CleanPath(p string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("path %q is not allowed in the work dir: %s", p, why)
	}
	if p == "" {
		return bad("empty")
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.Contains(p, ":") {
		return bad("absolute, a backslash or a ':'")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>"|?*`, r) {
			return bad("a control character or one of <>\"|?*")
		}
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return bad("outside the work dir")
	}
	for _, seg := range strings.Split(c, "/") {
		if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return bad("a name ending in a dot or a space")
		}
		stem := strings.ToUpper(seg)
		if i := strings.IndexByte(stem, '.'); i >= 0 {
			stem = stem[:i]
		}
		stem = strings.TrimRight(stem, " ")
		if windowsReserved[stem] {
			return bad("a Windows device name")
		}
	}
	return c, nil
}

// windowsReserved are the device names Windows reserves in every
// directory: CON, PRN, AUX, NUL, and COM and LPT followed by a digit 0-9
// or a superscript ¹, ² or ³.
var windowsReserved = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
	for _, d := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³"} {
		m["COM"+d] = true
		m["LPT"+d] = true
	}
	return m
}()

// CheckProjectName applies the sandbox rule to a project name, which
// becomes the directory and file projects/<name>/<name>.ap21: one path
// segment that CleanPath accepts.
func CheckProjectName(name string) error {
	c, err := CleanPath(name)
	if err != nil {
		return fmt.Errorf("project name: %w", err)
	}
	if c != name || strings.Contains(name, "/") {
		return fmt.Errorf("project name %q must be a single path segment", name)
	}
	return nil
}

// checkPaths returns a bad_request Error for the first path that breaks
// the sandbox rule.
func checkPaths(ps ...string) error {
	for _, p := range ps {
		if _, err := CleanPath(p); err != nil {
			return &Error{Kind: KindBadRequest, Message: err.Error()}
		}
	}
	return nil
}

func filePath(p string) (string, error) {
	c, err := CleanPath(p)
	if err != nil {
		return "", &Error{Kind: KindBadRequest, Message: err.Error()}
	}
	parts := strings.Split(c, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return "/v1/files/" + strings.Join(parts, "/"), nil
}

// PutFile writes data to path in the agent's work dir.
func (c *Client) PutFile(ctx context.Context, path string, data []byte) (FileInfo, error) {
	p, err := filePath(path)
	if err != nil {
		return FileInfo{}, err
	}
	var fi FileInfo
	_, err = c.do(ctx, http.MethodPut, p, bytes.NewReader(data), "application/octet-stream", &fi)
	return fi, err
}

// GetFile reads a file from the agent's work dir.
func (c *Client) GetFile(ctx context.Context, path string) ([]byte, error) {
	p, err := filePath(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+p, nil)
	if err != nil {
		return nil, fmt.Errorf("tiad: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, c.unauthorized()
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Failures are envelopes; the raw bytes are the success case only.
		var env struct {
			Error *errorWire `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			return nil, &Error{Kind: env.Error.Kind, Message: env.Error.Message, Fatal: env.Error.Fatal, Status: resp.StatusCode}
		}
		return nil, &Error{Kind: KindBadReply, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 400)), Status: resp.StatusCode}
	}
	return raw, nil
}

// DeleteFile removes a file from the agent's work dir.
func (c *Client) DeleteFile(ctx context.Context, path string) error {
	p, err := filePath(path)
	if err != nil {
		return err
	}
	_, err = c.doJSON(ctx, http.MethodDelete, p, nil, nil)
	return err
}

// ListFiles lists the agent's work dir.
func (c *Client) ListFiles(ctx context.Context) ([]FileInfo, error) {
	var out struct {
		Files []FileInfo `json:"files"`
	}
	_, err := c.doJSON(ctx, http.MethodGet, "/v1/files", nil, &out)
	return out.Files, err
}

// --- GSDML -------------------------------------------------------------------

// GSDInstall is the result of installing a GSDML file into TIA's catalogue.
type GSDInstall struct {
	Path             string `json:"path"`
	AlreadyInstalled bool   `json:"alreadyInstalled"`
}

// InstallGSD installs a GSDML file, already put in the work dir, into the
// hardware catalogue. Installing the same file twice is not an error.
func (c *Client) InstallGSD(ctx context.Context, path string) (GSDInstall, error) {
	if err := checkPaths(path); err != nil {
		return GSDInstall{}, err
	}
	var r GSDInstall
	_, err := c.doJSON(ctx, http.MethodPost, "/v1/gsd/install", map[string]string{"path": path}, &r)
	return r, err
}

// --- sessions ----------------------------------------------------------------

// Session is an open TIA project on the agent.
type Session struct {
	ID          string `json:"session"`
	Project     string `json:"project"`
	IdleSeconds int    `json:"idleSeconds,omitempty"`

	c *Client
}

// CreateProject builds a fresh project from a station spec and opens it.
// The station is validated here first, so a bad spec never reaches TIA.
func (c *Client) CreateProject(ctx context.Context, name string, st Station, sec Secrets) (*Session, error) {
	if err := CheckProjectName(name); err != nil {
		return nil, &Error{Kind: KindBadRequest, Message: err.Error()}
	}
	if err := st.Validate(); err != nil {
		return nil, &Error{Kind: KindBadRequest, Message: err.Error()}
	}
	if err := sec.CheckFor(st); err != nil {
		return nil, &Error{Kind: KindBadRequest, Message: err.Error()}
	}
	var s Session
	if _, err := c.doJSON(ctx, http.MethodPost, "/v1/projects",
		map[string]any{"name": name, "station": st, "secrets": sec}, &s); err != nil {
		return nil, err
	}
	s.c = c
	return &s, nil
}

// Open opens a project or project archive in the work dir.
func (c *Client) Open(ctx context.Context, path string) (*Session, error) {
	if err := checkPaths(path); err != nil {
		return nil, err
	}
	var s Session
	if _, err := c.doJSON(ctx, http.MethodPost, "/v1/sessions", map[string]string{"path": path}, &s); err != nil {
		return nil, err
	}
	s.c = c
	return &s, nil
}

// Sessions lists what the agent holds open.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	_, err := c.doJSON(ctx, http.MethodGet, "/v1/sessions", nil, &out)
	for i := range out.Sessions {
		out.Sessions[i].c = c
	}
	return out.Sessions, err
}

func (s *Session) path(op string) string {
	p := "/v1/sessions/" + url.PathEscape(s.ID)
	if op != "" {
		p += "/" + op
	}
	return p
}

// Close releases the project without saving. Safe to call twice.
func (s *Session) Close(ctx context.Context) error {
	_, err := s.c.doJSON(ctx, http.MethodDelete, s.path(""), nil, nil)
	if Kind(err) == KindNotFound {
		return nil
	}
	return err
}

// StationResult says what applying a station spec changed.
type StationResult struct {
	Changes []string `json:"changes"`
	// Notes are what the caller should know besides the changes: that a
	// CPU already protected kept its password, or that devices were kept
	// as found, for two.
	Notes []string `json:"notes,omitempty"`
	// KeptDevices are the project's devices besides the CPU's station
	// that an apply reusing the CPU kept as found. A download loads them
	// too, and lists them again.
	KeptDevices []KeptDevice `json:"keptDevices,omitempty"`
}

// ProjectCPU is a project's CPU as the project holds it: what a spec must
// name in cpu.name, cpu.order and cpu.firmware to reuse it.
type ProjectCPU struct {
	Name     string `json:"name"`
	Order    string `json:"order"`
	Firmware string `json:"firmware"`
}

// KeptDevice is a device in the project besides the CPU's station, kept
// as found by an apply that reused the CPU (tiad does not configure
// PROFINET IO devices yet). ProfinetName and Address are "" when it has
// none. A list holds one entry per network node, so a device with two
// interfaces appears twice under one Name: count names, not entries.
type KeptDevice struct {
	Name         string `json:"name"`
	ProfinetName string `json:"profinetName"`
	Address      string `json:"address"`
}

// ApplyStation makes the project's CPU match st. A project without a CPU
// gets one; a project whose CPU has st's name, order and firmware keeps
// it, with the devices besides it (KeptDevices); any other CPU refuses
// with KindCPUMismatch. tiad never replaces a project's CPU on an apply.
func (s *Session) ApplyStation(ctx context.Context, st Station, sec Secrets) (StationResult, error) {
	return s.ApplyStationWith(ctx, st, sec, ApplyOptions{})
}

// ApplyOptions are an apply's options besides the spec and its secrets.
type ApplyOptions struct {
	// FullAccessRecordOnly records secrets.fullAccessPassword as a found
	// Full access user's password without setting it (journalled): for a
	// project whose record tiad cannot verify (another agent token), when
	// TIA refuses the unchanged secret as one the user has. It trusts the
	// caller: naut sends it only on an operator's explicit action, never
	// as a retry default.
	FullAccessRecordOnly bool
}

// ApplyStationWith is ApplyStation with options.
func (s *Session) ApplyStationWith(ctx context.Context, st Station, sec Secrets, o ApplyOptions) (StationResult, error) {
	if err := st.Validate(); err != nil {
		return StationResult{}, &Error{Kind: KindBadRequest, Message: err.Error()}
	}
	if err := sec.CheckFor(st); err != nil {
		return StationResult{}, &Error{Kind: KindBadRequest, Message: err.Error()}
	}
	var r StationResult
	body := map[string]any{"station": st, "secrets": sec}
	if o.FullAccessRecordOnly {
		body["fullAccessRecordOnly"] = true
	}
	_, err := s.c.doJSON(ctx, http.MethodPut, s.path("station"), body, &r)
	return r, err
}

// SoftwareRequest imports SCL sources and tag tables, all already in the
// work dir, and generates blocks from them into the block group Group.
type SoftwareRequest struct {
	Group     string   `json:"group"`
	Sources   []string `json:"sources"`   // .scl, .db, .udt
	TagTables []string `json:"tagTables"` // tag table XML
	// Prune deletes blocks in Group that no source produced, so a block
	// removed from the nautilus project disappears from the CPU too.
	Prune bool `json:"prune"`
}

// SourceResult is the import outcome for one source file.
type SourceResult struct {
	Path     string   `json:"path"`
	OK       bool     `json:"ok"`
	Messages []string `json:"messages,omitempty"`
}

// Block is one block in the project.
type Block struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "OB" | "FB" | "FC" | "DB" | "UDT"
}

// SoftwareResult is the outcome of a software import.
type SoftwareResult struct {
	Results []SourceResult `json:"results"`
	Blocks  []Block        `json:"blocks"`
}

func (s *Session) Software(ctx context.Context, req SoftwareRequest) (SoftwareResult, error) {
	if err := checkPaths(append(append([]string{}, req.Sources...), req.TagTables...)...); err != nil {
		return SoftwareResult{}, err
	}
	var r SoftwareResult
	_, err := s.c.doJSON(ctx, http.MethodPost, s.path("software"), req, &r)
	return r, err
}

// CompileMessage is one compiler message. Object is TIA's path to the
// offending object; the writer maps it back to a nautilus source line.
type CompileMessage struct {
	Object   string `json:"object"`
	Severity string `json:"severity"` // "error" | "warning" | "info"
	Text     string `json:"text"`
}

// CompileResult is a compile's outcome.
type CompileResult struct {
	Result   string           `json:"result"` // "success" | "warning" | "error"
	Errors   int              `json:"errors"`
	Warnings int              `json:"warnings"`
	Messages []CompileMessage `json:"messages"`
}

// Compile compiles the hardware config, the software, or both. Errors come
// back as a compile_failed error carrying the result (see CompileErrors).
func (s *Session) Compile(ctx context.Context, hardware, software bool) (CompileResult, error) {
	var r CompileResult
	_, err := s.c.doJSON(ctx, http.MethodPost, s.path("compile"), map[string]bool{"hardware": hardware, "software": software}, &r)
	return r, err
}

// Download loads the project into the CPU at req.Address. See Resolve for
// how questions are answered. A dry run lists the questions and cancels;
// it reaches the CPU too, so it must be announced like a real download,
// and that it loads nothing is provisional until S2 proves it
// (siemens-ide.md §3.5; the result says Provisional).
//
// When OutcomeUnknown(err) (the agent did not answer, ctx was cancelled,
// or the agent answered outcome_unknown), the CPU may or may not be
// loaded. Run Compare on the same session before anything else, never
// retry blindly.
func (s *Session) Download(ctx context.Context, req DownloadRequest) (DownloadResult, error) {
	if err := req.Validate(); err != nil {
		return DownloadResult{}, &Error{Kind: KindBadRequest, Message: err.Error()}
	}
	var r DownloadResult
	_, err := s.c.doJSON(ctx, http.MethodPost, s.path("download"), req, &r)
	return r, err
}

// Difference is one object that differs between the project and the CPU.
type Difference struct {
	Object string `json:"object"`
	State  string `json:"state"` // e.g. "different", "only offline", "only online"
}

// CompareResult is an online compare.
type CompareResult struct {
	Equal       bool         `json:"equal"`
	Differences []Difference `json:"differences"`
}

// Compare compares the project with the CPU it was downloaded to.
func (s *Session) Compare(ctx context.Context) (CompareResult, error) {
	var r CompareResult
	_, err := s.c.doJSON(ctx, http.MethodPost, s.path("compare"), struct{}{}, &r)
	return r, err
}

// ExportRequest asks for files written under Dir in the work dir.
type ExportRequest struct {
	Dir       string `json:"dir"`
	Blocks    string `json:"blocks,omitempty"` // "scl" | "xml" | "" (none)
	TagTables bool   `json:"tagTables,omitempty"`
	Hardware  bool   `json:"hardware,omitempty"`
	GSDML     bool   `json:"gsdml,omitempty"` // the CPU's own GSDML as an I-device
}

// ExportResult lists the files written.
type ExportResult struct {
	Files []string `json:"files"`
}

func (s *Session) Export(ctx context.Context, req ExportRequest) (ExportResult, error) {
	if err := checkPaths(req.Dir); err != nil {
		return ExportResult{}, err
	}
	var r ExportResult
	_, err := s.c.doJSON(ctx, http.MethodPost, s.path("export"), req, &r)
	return r, err
}

// Upload loads the CPU's program into the session's project, for bringing
// a running CPU's program into nautilus. It trusts no CPU certificate.
func (s *Session) Upload(ctx context.Context) error {
	_, err := s.UploadWith(ctx, UploadRequest{})
	return err
}

// UploadRequest is an upload's options.
type UploadRequest struct {
	// TrustUnverifiedCertificate answers TIA's TLS question going online
	// to the CPU "trusted", blindly: TIA shows no certificate to check. An
	// explicit, per-call opt-in for a read-only upload from a CPU in
	// TIAD_TARGETS; credentials and other questions still cancel. Download
	// and compare have no such option.
	TrustUnverifiedCertificate bool `json:"trustUnverifiedCertificate,omitempty"`
}

// UploadResult is what an upload did.
type UploadResult struct {
	// TrustedBlindly are the certificate questions answered trusted on
	// the request's opt-in.
	TrustedBlindly []string `json:"trustedBlindly"`
	// Device is the CPU's identity as the upload read it.
	Device DeviceIdentity `json:"device"`
	// Pin is PinFirstTrust when this upload pinned the CPU (no pin
	// existed), or PinMatched. A different identity fails the upload
	// with KindDeviceChanged and leaves the pin as it was.
	Pin string `json:"pin"`
	// CPU is the uploaded CPU as the project now holds it: a spec that
	// names its name, order and firmware reuses it on the next apply. It
	// is zero when the project's CPU could not be read, and CPUError says
	// why (a project with two CPUs, for one); the next apply refuses too.
	CPU      ProjectCPU `json:"cpu"`
	CPUError string     `json:"cpuError,omitempty"`
	// RemovedAccess names what applies on this session had made in the
	// project's users and roles (`user "nautilus"`, `role "nautilus full
	// access"`), which the upload removed with the station they belonged
	// to. A user or role the project held before is never removed.
	RemovedAccess []string `json:"removedAccess,omitempty"`
}

// UploadWith is Upload with options.
func (s *Session) UploadWith(ctx context.Context, req UploadRequest) (UploadResult, error) {
	var r UploadResult
	_, err := s.c.doJSON(ctx, http.MethodPost, s.path("upload"), req, &r)
	return r, err
}

// Save saves the project, or with archive set writes a project archive,
// and returns its work-dir path.
func (s *Session) Save(ctx context.Context, archive bool) (string, error) {
	var r struct {
		Path string `json:"path"`
	}
	_, err := s.c.doJSON(ctx, http.MethodPost, s.path("save"), map[string]bool{"archive": archive}, &r)
	return r.Path, err
}
