// Package logixd is the Go client for the logixd agent — nautilus's handle
// on a Logix project.
//
// The Studio 5000 SDK is a gRPC service bound to 127.0.0.1, reachable only
// from .NET or pythonnet (docs/design/logix-sdk-api.md §2). There is no
// remote protocol and no Go binding. So nautilus does not talk to the SDK;
// it talks to `logixd`, a small agent co-resident with the licensed Windows
// install (tools/logixd), which owns the SDK's concurrency and licensing
// rules and speaks JSON over HTTP.
//
// Everything here is a thin, typed mapping of that surface. The interesting
// design is in two places:
//
//   - Error is faithful to the SDK's own distinction (§7): an
//     OperationFailed means the request or the controller's state was
//     wrong and the caller can act on it; an OperationNotPerformed means
//     the SDK or the channel broke and the session is dead. Fatal carries
//     that across the wire so a caller can tell "fix your XPath" from
//     "give up on this session".
//   - Every reply carries the SDK's event stream. A failed partial import
//     says almost nothing through its error and a great deal through its
//     events, so they ride along on success and failure alike.
package logixd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultURL is where a loopback agent listens.
const DefaultURL = "http://127.0.0.1:8188"

// Client talks to one logixd agent.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a client. An empty baseURL falls back to NAUTILUS_LOGIXD_URL
// and then to DefaultURL; an empty token to NAUTILUS_LOGIXD_TOKEN.
func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("NAUTILUS_LOGIXD_URL")
	}
	if baseURL == "" {
		baseURL = DefaultURL
	}
	if token == "" {
		token = os.Getenv("NAUTILUS_LOGIXD_TOKEN")
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		// Generous: opening a 500 MB project, a build, or a download are
		// all minutes-long operations. Callers bound them with a context.
		HTTP: &http.Client{Timeout: 30 * time.Minute},
	}
}

// Event is one entry from the SDK's own operation-event stream.
type Event struct {
	Kind    string `json:"kind"` // "status" | "error" | "progress"
	Source  string `json:"source"`
	Message string `json:"message"`
	Percent int    `json:"percent"`
}

func (e Event) String() string {
	if e.Kind == "progress" {
		return fmt.Sprintf("%s: %d%%", e.Source, e.Percent)
	}
	if e.Source == "" {
		return fmt.Sprintf("%s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("%s [%s]: %s", e.Kind, e.Source, e.Message)
}

// Error is a failure reported by the agent or the SDK beneath it.
type Error struct {
	Kind    string  `json:"kind"`
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Fatal   bool    `json:"fatal"`
	Status  int     `json:"-"`
	Events  []Event `json:"-"`
}

func (e *Error) Error() string {
	if e.Type != "" && e.Type != e.Kind {
		return fmt.Sprintf("logixd: %s (%s): %s", e.Kind, e.Type, e.Message)
	}
	return fmt.Sprintf("logixd: %s: %s", e.Kind, e.Message)
}

// Fatal reports whether the session (or the agent) should be considered
// dead. An OperationFailed is the caller's problem to fix and is not fatal;
// an OperationNotPerformed means the SDK or the channel broke.
func (e *Error) Fatal_() bool { return e.Fatal }

// IsFatal reports whether err is a logixd error that killed the session.
func IsFatal(err error) bool {
	var e *Error
	if ok := asError(err, &e); ok {
		return e.Fatal
	}
	return false
}

// Kind returns a logixd error's kind, or "" for any other error.
func Kind(err error) string {
	var e *Error
	if ok := asError(err, &e); ok {
		return e.Kind
	}
	return ""
}

func asError(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// envelope is the agent's uniform reply shape.
type envelope struct {
	OK     bool            `json:"ok"`
	Data   json.RawMessage `json:"data"`
	Error  *Error          `json:"error"`
	Events []Event         `json:"events"`
}

// Result pairs a decoded payload's events with the call that produced them.
type Result struct {
	Events []Event
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) ([]Event, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("logixd: encoding request: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("logixd: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// A dead agent is fatal by definition, and the message should say
		// where nautilus was looking — a wrong URL is the usual cause.
		return nil, &Error{
			Kind:    "unreachable",
			Message: fmt.Sprintf("%s: %v", c.BaseURL, err),
			Fatal:   true,
		}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &Error{Kind: "unreachable", Message: err.Error(), Fatal: true}
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &Error{
			Kind:    "bad_reply",
			Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 400)),
			Status:  resp.StatusCode,
			Fatal:   true,
		}
	}
	if !env.OK || env.Error != nil {
		e := env.Error
		if e == nil {
			e = &Error{Kind: "unknown", Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		e.Status = resp.StatusCode
		e.Events = env.Events
		return env.Events, e
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return env.Events, fmt.Errorf("logixd: decoding %s reply: %w", path, err)
		}
	}
	return env.Events, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- health and licensing -------------------------------------------------

// Health is the agent's self-report.
type Health struct {
	Service       string   `json:"service"`
	Version       string   `json:"version"`
	SDKClient     string   `json:"sdkClient"`
	Sessions      int      `json:"sessions"`
	CommAllowlist []string `json:"commAllowlist"`
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	_, err := c.do(ctx, http.MethodGet, "/v1/health", nil, &h)
	return h, err
}

// Gate is one licensing or prerequisite check.
type Gate struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Remedy is what to DO about a failing gate, sent by the agent. Detail
	// says what is wrong; this says what to try, in the order worth trying.
	// Empty on a gate that passed.
	Remedy string `json:"remedy,omitempty"`
}

// Probe is the result of the agent's licensing check.
type Probe struct {
	Usable bool   `json:"usable"`
	Gates  []Gate `json:"gates"`
	Hint   string `json:"hint"`
}

// Probe asks the agent whether the SDK is actually usable, and if not,
// which gate failed.
//
// This exists because the SDK needs three independent things — an FTSP auth
// token, a FlexNet feature, and a CodeMeter entitlement — and each fails
// with a different, uninformative message. Worse, whichever fails first
// masks the others, so you fix one and discover the next. Reporting them
// individually is a product requirement, not a diagnostic nicety.
// A probe that answers "not usable" is a SUCCESSFUL probe — err is nil and
// Usable is false. An error here means the agent could not be reached or
// could not run the check at all.
func (c *Client) Probe(ctx context.Context) (Probe, error) {
	var p Probe
	_, err := c.do(ctx, http.MethodGet, "/v1/probe", nil, &p)
	return p, err
}

// --- sessions -------------------------------------------------------------

// Session is an open project on the agent.
type Session struct {
	ID       string    `json:"session"`
	Project  string    `json:"project"`
	OpenedAt time.Time `json:"openedAt"`
	LastUsed time.Time `json:"lastUsed"`

	c *Client
}

// Open opens a project on the agent. The caller must Close it; the agent
// also reaps idle sessions, because a leaked session pins a project
// server-side.
func (c *Client) Open(ctx context.Context, projectPath string) (*Session, error) {
	var s Session
	if _, err := c.do(ctx, http.MethodPost, "/v1/sessions",
		map[string]any{"project": projectPath}, &s); err != nil {
		return nil, err
	}
	s.c = c
	return &s, nil
}

// Sessions lists what the agent currently holds open.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out []Session
	_, err := c.do(ctx, http.MethodGet, "/v1/sessions", nil, &out)
	return out, err
}

// Close releases the project. Safe to call twice.
func (s *Session) Close(ctx context.Context) error {
	_, err := s.c.do(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(s.ID), nil, nil)
	if Kind(err) == "not_found" {
		return nil
	}
	return err
}

func (s *Session) path(suffix string) string {
	return "/v1/sessions/" + url.PathEscape(s.ID) + suffix
}

// --- whole-project operations ---------------------------------------------

// ConvertResult reports a finished format conversion.
type ConvertResult struct {
	Input       string `json:"input"`
	Output      string `json:"output"`
	Bytes       int64  `json:"bytes"`
	DetailedL5X bool   `json:"detailedL5x"`
}

// Convert turns one project file into another by extension: ACD, L5K or
// L5X, in any direction. It is two SDK calls — Open accepts all three and
// SaveAs writes whichever the extension names.
//
// detailedL5x controls the L5X ExportOptions the SDK writes (References,
// Context, ProductDefinedTypes, IOTags). False is the lean export, and the
// lean export is the one that diffs well, so it is the default everywhere
// in nautilus.
func (c *Client) Convert(ctx context.Context, input, output string, detailedL5X bool) (ConvertResult, []Event, error) {
	var r ConvertResult
	ev, err := c.do(ctx, http.MethodPost, "/v1/convert", map[string]any{
		"input": input, "output": output, "force": true, "detailedL5x": detailedL5X,
	}, &r)
	return r, ev, err
}

// CreateProject makes a new, empty project file.
func (c *Client) CreateProject(ctx context.Context, path string, majorRev uint32, processorType, controllerName string) ([]Event, error) {
	return c.do(ctx, http.MethodPost, "/v1/create", map[string]any{
		"project": path, "majorRevision": majorRev,
		"processorType": processorType, "controllerName": controllerName,
	}, nil)
}

// Save writes the open project back. An empty path saves in place;
// otherwise it saves as the format the extension names.
func (s *Session) Save(ctx context.Context, path string, detailedL5X bool) ([]Event, error) {
	return s.c.do(ctx, http.MethodPost, s.path("/save"), map[string]any{
		"path": path, "force": true, "detailedL5x": detailedL5X,
	}, nil)
}

// BuildTarget selects what BuildAsync compiles for. It must match the
// controller the project will be downloaded to, or the compile happens
// twice.
type BuildTarget string

const (
	BuildDefault  BuildTarget = "DefaultTarget"
	BuildPhysical BuildTarget = "PhysicalController"
	BuildEcho     BuildTarget = "EchoController"
)

// BuildResult reports a finished build.
type BuildResult struct {
	Target    string `json:"target"`
	ElapsedMs int64  `json:"elapsedMs"`
}

// Build compiles the controller's routines and caches the binaries in the
// project file. This is the call that makes "CI for control logic" real:
// it needs no controller, carries no risk, and catches what "it compiled on
// my machine" never does. Requires a Logix Designer v37+ project.
func (s *Session) Build(ctx context.Context, target BuildTarget) (BuildResult, []Event, error) {
	var r BuildResult
	ev, err := s.c.do(ctx, http.MethodPost, s.path("/build"),
		map[string]any{"target": string(target)}, &r)
	return r, ev, err
}

// Executables returns an XPath for every routine and AOI in the project —
// the only browse-shaped call the SDK has.
func (s *Session) Executables(ctx context.Context) ([]string, error) {
	var r struct {
		Executables []string `json:"executables"`
	}
	_, err := s.c.do(ctx, http.MethodGet, s.path("/executables"), nil, &r)
	return r.Executables, err
}

// --- partial import and export --------------------------------------------

// PartialExport writes the component(s) an XPath selects to an L5X file.
// Works online or offline.
func (s *Session) PartialExport(ctx context.Context, xpath, output string) ([]Event, error) {
	return s.c.do(ctx, http.MethodPost, s.path("/partial-export"),
		map[string]any{"xpath": xpath, "output": output, "force": true}, nil)
}

// CollisionOption decides what an offline import does when the thing it is
// creating already exists.
type CollisionOption string

const (
	Overwrite CollisionOption = "OverwriteOnColl"
	Discard   CollisionOption = "DiscardOnColl"
	Cancel    CollisionOption = "CancelOnColl"
)

// PartialImport merges an L5X into the project. OFFLINE ONLY — use
// ImportWithTarget or ImportRungs to change a running controller.
func (s *Session) PartialImport(ctx context.Context, xpath, file string, collision CollisionOption, continueOnErrors bool) ([]Event, error) {
	return s.c.do(ctx, http.MethodPost, s.path("/partial-import"), map[string]any{
		"xpath": xpath, "file": file,
		"collision": string(collision), "continueOnErrors": continueOnErrors,
	}, nil)
}

// ImportOption is the online-edit workflow, and it is the capability that
// makes a warm change to a running Logix controller possible at all.
//
// It is ignored for an offline import.
type ImportOption string

const (
	// LeaveEdits leaves the change as offline pending edits — test.
	LeaveEdits ImportOption = "LeaveEdits"
	// AcceptEdits accepts the change and sends it down to the controller.
	AcceptEdits ImportOption = "AcceptEdits"
	// FinalizeEdits accepts, sends down, and — if the controller is in Run
	// — assembles. This is the whole online-edit cycle in one call.
	FinalizeEdits ImportOption = "FinalizeEdits"
)

// ImportWithTarget imports one component, optionally renaming it. Works
// online and offline; rungs are NOT allowed here (use ImportRungs).
func (s *Session) ImportWithTarget(ctx context.Context, xpath, targetName, file string, opt ImportOption) ([]Event, error) {
	return s.c.do(ctx, http.MethodPost, s.path("/partial-import-with-target"), map[string]any{
		"xpath": xpath, "targetName": targetName, "file": file, "onlineOption": string(opt),
	}, nil)
}

// ImportRungsResult reports a finished rung import.
type ImportRungsResult struct {
	XPath          string `json:"xpath"`
	InsertPosition uint32 `json:"insertPosition"`
	ReplaceCount   uint32 `json:"replaceCount"`
	OnlineOption   string `json:"onlineOption"`
	ElapsedMs      int64  `json:"elapsedMs"`
}

// ImportRungs replaces replaceCount rungs at insertPosition in an RLL
// routine with the rungs in an L5X file.
//
// Online, with AcceptEdits or FinalizeEdits, this is a warm change to a
// running controller — the finest-grained write the SDK offers, and finer
// than most people drive the GUI.
func (s *Session) ImportRungs(ctx context.Context, xpath string, insertPosition, replaceCount uint32, file string, opt ImportOption) (ImportRungsResult, []Event, error) {
	var r ImportRungsResult
	ev, err := s.c.do(ctx, http.MethodPost, s.path("/import-rungs"), map[string]any{
		"xpath": xpath, "insertPosition": insertPosition, "replaceCount": replaceCount,
		"file": file, "onlineOption": string(opt),
	}, &r)
	return r, ev, err
}

// --- controller -----------------------------------------------------------

// State is the project's relationship to a controller.
type State struct {
	Connected string `json:"connected"` // Unknown | Offline | Connected | Online
	CommPath  string `json:"commPath"`
}

// SetCommPath points the project at a controller. The agent checks the path
// against its own allowlist; a caller cannot name a controller the operator
// has not blessed.
func (s *Session) SetCommPath(ctx context.Context, path string) (State, error) {
	var st State
	_, err := s.c.do(ctx, http.MethodPost, s.path("/comm-path"), map[string]any{"path": path}, &st)
	return st, err
}

// State reads the connection state and comm path.
func (s *Session) State(ctx context.Context) (State, error) {
	var st State
	_, err := s.c.do(ctx, http.MethodGet, s.path("/state"), nil, &st)
	return st, err
}

// GoOnline connects the project to its controller.
func (s *Session) GoOnline(ctx context.Context) (State, error) {
	var st State
	_, err := s.c.do(ctx, http.MethodPost, s.path("/online"), nil, &st)
	return st, err
}

// GoOffline disconnects.
func (s *Session) GoOffline(ctx context.Context) (State, error) {
	var st State
	_, err := s.c.do(ctx, http.MethodPost, s.path("/offline"), nil, &st)
	return st, err
}

// ControllerMode is a mode read back from the controller.
type ControllerMode string

// RequestedMode is a mode to put the controller into.
type RequestedMode string

const (
	ModeRun     RequestedMode = "Run"
	ModeProgram RequestedMode = "Program"
	ModeTest    RequestedMode = "Test"
)

// Mode reads the controller's current mode.
func (s *Session) Mode(ctx context.Context) (ControllerMode, error) {
	var r struct {
		Mode string `json:"mode"`
	}
	_, err := s.c.do(ctx, http.MethodGet, s.path("/mode"), nil, &r)
	return ControllerMode(r.Mode), err
}

// SetMode changes the controller's mode.
func (s *Session) SetMode(ctx context.Context, m RequestedMode) (ControllerMode, error) {
	var r struct {
		Mode string `json:"mode"`
	}
	_, err := s.c.do(ctx, http.MethodPost, s.path("/mode"), map[string]any{"mode": string(m)}, &r)
	return ControllerMode(r.Mode), err
}

// Download sends the whole project to the controller.
//
// THIS STOPS THE CONTROLLER AND RESETS ITS TAGS TO PROJECT VALUES. Unlike
// the Logix Designer GUI, the SDK neither changes the mode for you nor
// checks that it is right, so ensureProgramMode is explicit and off by
// default: nobody should stop a line by omission.
func (s *Session) Download(ctx context.Context, ensureProgramMode bool) ([]Event, error) {
	return s.c.do(ctx, http.MethodPost, s.path("/download"),
		map[string]any{"ensureProgramMode": ensureProgramMode}, nil)
}

// UploadToNew uploads what is actually in a controller into a new project
// file. This is the read side of drift detection, and the rollback artifact
// to take before any download.
func (c *Client) UploadToNew(ctx context.Context, commPath, output string) ([]Event, error) {
	return c.do(ctx, http.MethodPost, "/v1/upload-to-new",
		map[string]any{"commPath": commPath, "output": output}, nil)
}

// --- tag values -----------------------------------------------------------

// TagMode selects whether a tag operation touches the project file or the
// controller.
type TagMode string

const (
	// Offline reads and writes the value stored in the project file — no
	// controller needed.
	Offline TagMode = "Offline"
	// Online goes to the controller, and costs about half a second per
	// tag. Never put anything at scan rate through it; that is what the
	// EtherNet/IP driver is for.
	Online TagMode = "Online"
)

// GetTag reads one tag. typ is a Logix elementary type name (BOOL, DINT,
// REAL, STRING, …).
func (s *Session) GetTag(ctx context.Context, tagPath, typ string, mode TagMode) (any, error) {
	var r struct {
		Value any `json:"value"`
	}
	_, err := s.c.do(ctx, http.MethodPost, s.path("/tag/get"),
		map[string]any{"tagPath": tagPath, "type": typ, "mode": string(mode)}, &r)
	return r.Value, err
}

// SetTag writes one tag.
func (s *Session) SetTag(ctx context.Context, tagPath, typ string, mode TagMode, value any) error {
	_, err := s.c.do(ctx, http.MethodPost, s.path("/tag/set"),
		map[string]any{"tagPath": tagPath, "type": typ, "mode": string(mode), "value": value}, nil)
	return err
}

// TagPath builds the XPath a tag operation wants for a controller-scoped
// tag. The SDK addresses tags by XPath, not by name, and getting the quoting
// wrong is a silent "tag not found".
func TagPath(name string) string {
	return fmt.Sprintf("Controller/Tags/Tag[@Name='%s']", name)
}

// ProgramTagPath is TagPath for a program-scoped tag.
func ProgramTagPath(program, name string) string {
	return fmt.Sprintf("Controller/Programs/Program[@Name='%s']/Tags/Tag[@Name='%s']", program, name)
}

// RoutinePath builds the XPath that addresses one routine — what
// ImportRungs and PartialExport take.
func RoutinePath(program, routine string) string {
	return fmt.Sprintf("Controller/Programs/Program[@Name='%s']/Routines/Routine[@Name='%s']", program, routine)
}

// ProgramPath builds the XPath that addresses one program.
func ProgramPath(program string) string {
	return fmt.Sprintf("Controller/Programs/Program[@Name='%s']", program)
}

// --- file transfer --------------------------------------------------------
//
// Every path the agent touches on a caller's behalf is relative to one work
// directory it owns. That is not bureaucracy: ImportRungs takes a path ON
// THE AGENT, so a caller on another machine has to put the L5X there first
// and fetch an upload back afterwards. An agent that read and wrote any
// path it was handed would be a file server with a controller attached.

// WorkDir returns the agent's work directory (informational; all file paths
// in this API are relative to it).
func (c *Client) WorkDir(ctx context.Context) (string, error) {
	var r struct {
		WorkDir string `json:"workDir"`
	}
	_, err := c.do(ctx, http.MethodGet, "/v1/workdir", nil, &r)
	return r.WorkDir, err
}

// RemoteFile describes one file in the agent's work directory.
type RemoteFile struct {
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
}

// ListFiles enumerates the agent's work directory.
func (c *Client) ListFiles(ctx context.Context) ([]RemoteFile, error) {
	var r struct {
		Files []RemoteFile `json:"files"`
	}
	_, err := c.do(ctx, http.MethodGet, "/v1/files", nil, &r)
	return r.Files, err
}

// PutFile uploads content to relPath inside the agent's work directory.
func (c *Client) PutFile(ctx context.Context, relPath string, content []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.BaseURL+"/v1/files/"+escapePath(relPath), bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("logixd: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &Error{Kind: "unreachable", Message: err.Error(), Fatal: true}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		if env.Error != nil {
			env.Error.Status = resp.StatusCode
			return env.Error
		}
		return &Error{Kind: "bad_reply", Message: truncate(string(raw), 300), Status: resp.StatusCode, Fatal: true}
	}
	return nil
}

// GetFile downloads relPath from the agent's work directory.
func (c *Client) GetFile(ctx context.Context, relPath string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/v1/files/"+escapePath(relPath), nil)
	if err != nil {
		return nil, fmt.Errorf("logixd: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &Error{Kind: "unreachable", Message: err.Error(), Fatal: true}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &Error{Kind: "unreachable", Message: err.Error(), Fatal: true}
	}
	if resp.StatusCode != http.StatusOK {
		var env envelope
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			env.Error.Status = resp.StatusCode
			return nil, env.Error
		}
		return nil, &Error{Kind: "bad_reply", Message: truncate(string(raw), 300), Status: resp.StatusCode}
	}
	return raw, nil
}

// DeleteFile removes relPath from the agent's work directory.
func (c *Client) DeleteFile(ctx context.Context, relPath string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/files/"+escapePath(relPath), nil, nil)
	return err
}

// escapePath escapes each segment but keeps the separators, so a nested
// relative path survives as a path rather than becoming one escaped blob.
func escapePath(p string) string {
	parts := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
