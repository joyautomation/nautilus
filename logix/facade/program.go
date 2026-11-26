package facade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/logix/deploy"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// The program plane: the editor's Download button on a Logix target.
//
// With a ProgramPlane, GET /api/program answers with the ladder source the
// controller was last deployed from, and PUT /api/program runs the deploy
// flow (logix/deploy) as an ONLINE EDIT: the writer, the SDK import and
// build, the logic diff against the running controller, the rung import
// with FinalizeEdits, and the verification upload. The extension needs no
// change: it composes the workspace, PUTs {source, baseHash}, and shows
// the error verbatim when the controller refuses — which here is how a
// change that needs a download (new tags) reaches the author, naming the
// command that does it.
//
// POST /api/logix/deploy is the same flow with its progress streamed, and
// the one route to a DOWNLOAD from the editor: {mode: "online"|"download",
// source, baseHash, programMode, resetValues, yes}. A download stops the
// controller and, unless resetValues is set, keeps its tag values (the
// rule is logix/deploy's PlanValues), so
// it needs "yes": true in the body — the extension sends it only after a
// modal that says so — and it is never what the plain PUT does. The answer
// is NDJSON: {"event":"progress","message":…} lines as the deploy logs,
// then one {"event":"done",…report} or {"event":"error","status":…,
// "error":…}. Refusals that need no controller (a stale base, a deploy
// already running, no "yes") are ordinary HTTP errors before the stream.
//
// The headers go out at once, and the report goes out the moment the
// deploy has verified. A download's stream may go on after its report:
// Warm gets the next online edit ready (an online session takes most of a
// minute to open), its progress lines following the report, while the
// plane already takes the next deploy. A report's elapsedMs is the whole
// wait from the request to the report — the session the first edit
// opens, the warm-up an edit waited for — and deployMs the deploy's own
// part of it.

// DownloadOptions are the choices a download request carries.
type DownloadOptions struct {
	// ProgramMode asks for Program mode before the download.
	ProgramMode bool
	// ResetValues starts every tag at the project's value instead of
	// keeping the controller's.
	ResetValues bool
}

// ProgramPlane is the deployable program behind the program endpoints.
type ProgramPlane struct {
	// Source is the ladder program the controller runs now — the repo's
	// file at startup. Its hash is what the editor's baseHash must match.
	Source string
	// Deploy runs an online edit of src and reports, sending progress
	// lines to progress (never nil). Errors of type *deploy.DiagError,
	// *deploy.NeedsDownloadError and *deploy.VerifyError are answered
	// with their own status codes.
	Deploy func(ctx context.Context, src string, progress func(string)) (*deploy.Report, error)
	// Download, when set, sends src as a whole project — stopping the
	// controller — and reports. Nil leaves POST /api/logix/deploy's
	// download mode refused.
	Download func(ctx context.Context, src string, o DownloadOptions, progress func(string)) (*deploy.Report, error)
	// Warm, when set, runs after a download's report is sent, on the
	// same stream: what the next online edit needs ready (the serve opens
	// its online session here). The plane is free while it runs, so a
	// Deploy that arrives meanwhile must wait for it.
	Warm func(ctx context.Context, progress func(string))
	// Controller and CommPath name the target on GET /api/program, so a
	// confirmation can say where a download goes.
	Controller, CommPath string
	// Timeout bounds one deploy (default 15 minutes).
	Timeout time.Duration
}

type programPlane struct {
	cfg ProgramPlane
	// auth is the server's authorization for perm (server.Authorize):
	// an online edit needs edit, a download deploy (#708).
	auth   func(r *http.Request, perm string) (int, string)
	mu     sync.Mutex
	source string
	busy   bool
	last   *deploy.Report
	lastAt time.Time
}

func sourceHash(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:6])
}

// programInfo mirrors server's GET /api/program shape, with what is true
// of a deployed ladder program.
type programInfo struct {
	Task        string `json:"task"`
	POU         string `json:"pou"`
	Source      string `json:"source"`
	Language    string `json:"language"`
	Hash        string `json:"hash"`
	Dirty       bool   `json:"dirty"`
	Editable    bool   `json:"editable"`
	CanRollback bool   `json:"canRollback"`
	CompiledAt  int64  `json:"compiledAt"`
	Scans       uint64 `json:"scans"`
	Error       string `json:"error,omitempty"`
	// Target is "logix": the editor's cue that Download means an online
	// edit here and that POST /api/logix/deploy exists.
	Target      string `json:"target"`
	Controller  string `json:"controller,omitempty"`
	CommPath    string `json:"commPath,omitempty"`
	CanDownload bool   `json:"canDownload"`
}

func (p *programPlane) get(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	info := programInfo{
		Task: "main", POU: runtime.POUOf(p.source), Source: p.source, Language: "ld",
		Hash: sourceHash(p.source), Editable: true,
		Target: "logix", Controller: p.cfg.Controller, CommPath: p.cfg.CommPath, CanDownload: p.cfg.Download != nil,
	}
	if !p.lastAt.IsZero() {
		info.CompiledAt = p.lastAt.UnixMilli()
	}
	if p.busy {
		info.Error = "a deploy is in progress"
	}
	writeAny(w, http.StatusOK, info)
}

// auditTarget names what a program write acts on for the journal: the
// controller, the hash it runs now, and a hash of the source sent (never
// the source itself, which can be large).
func (p *programPlane) auditTarget(deploy bool) func(*http.Request, []byte) server.AuditTarget {
	return func(_ *http.Request, body []byte) server.AuditTarget {
		var req struct {
			Mode   string `json:"mode"`
			Source string `json:"source"`
		}
		_ = json.Unmarshal(body, &req)
		p.mu.Lock()
		before := sourceHash(p.source)
		p.mu.Unlock()
		t := server.AuditTarget{Target: "main", Before: before}
		if p.cfg.Controller != "" {
			t.Target = p.cfg.Controller
		}
		if req.Source != "" {
			sum := sha256.Sum256([]byte(req.Source))
			t.After = hex.EncodeToString(sum[:6])
			t.Detail = map[string]any{"sha256": hex.EncodeToString(sum[:]), "bytes": len(req.Source)}
		}
		if deploy {
			if t.Detail == nil {
				t.Detail = map[string]any{}
			}
			mode := "other" // the client's own words are never journalled
			if req.Mode == "online" || req.Mode == "download" {
				mode = req.Mode
			}
			t.Detail["mode"] = mode
		}
		return t
	}
}

func (p *programPlane) put(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if code, msg := p.auth(r, server.PermEdit); code != 0 {
		server.WriteRefusal(w, code, msg)
		return
	}
	var req struct {
		Source   string `json:"source"`
		BaseHash string `json:"baseHash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Source == "" {
		writeJSON(w, http.StatusBadRequest, "body must be {\"source\": \"...\"}")
		return
	}
	if !p.begin(w, req.BaseHash) {
		return
	}
	defer p.end()
	ctx, cancel := context.WithTimeout(r.Context(), p.timeout())
	defer cancel()
	rep, err := p.cfg.Deploy(ctx, req.Source, func(string) {})
	if err != nil {
		code, msg := deployError(err)
		writeJSON(w, code, msg)
		return
	}
	writeAny(w, http.StatusOK, p.applied(req.Source, rep, started))
}

// begin claims the plane for one deploy, or answers why not: another
// deploy is running, or the editor's base is not what the controller runs.
func (p *programPlane) begin(w http.ResponseWriter, baseHash string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		writeJSON(w, http.StatusConflict, "a deploy is already in progress on this controller")
		return false
	}
	if baseHash != "" && baseHash != sourceHash(p.source) {
		writeAny(w, http.StatusConflict, map[string]string{
			"error": "controller program changed since your base — refresh and re-apply", "hash": sourceHash(p.source), "task": "main",
		})
		return false
	}
	p.busy = true
	return true
}

func (p *programPlane) end() {
	p.mu.Lock()
	p.busy = false
	p.mu.Unlock()
}

func (p *programPlane) timeout() time.Duration {
	if p.cfg.Timeout == 0 {
		return 15 * time.Minute
	}
	return p.cfg.Timeout
}

// applied records a successful deploy of src and returns the answer body.
// elapsedMs is the wait since the request arrived, started; deployMs is
// the deploy's own time.
func (p *programPlane) applied(src string, rep *deploy.Report, started time.Time) map[string]any {
	p.mu.Lock()
	p.source = src
	p.last = rep
	p.lastAt = time.Now()
	hash := sourceHash(p.source)
	p.mu.Unlock()
	body := map[string]any{
		"hash": hash, "task": "main", "applied": rep.Applied.String(), "same": rep.Same,
		"replaced": rep.Replaced, "verified": rep.Verified, "modeBefore": rep.ModeBefore, "mode": rep.ModeAfter,
		"elapsedMs": time.Since(started).Milliseconds(), "deployMs": rep.Elapsed.Milliseconds(),
	}
	// valuesKept and resets are a download's: an online edit carries every
	// value, so it has nothing to say about them (#391).
	if rep.Applied == deploy.Download {
		body["resets"] = resets(rep)
		body["valuesKept"] = valuesKept(rep)
	}
	return body
}

// resets are the tags whose values a download put back to the project's,
// each with its reason; valuesKept counts the ones it kept, -1 when the
// values were not kept at all (an online edit, or a download asked to
// reset them).
func resets(rep *deploy.Report) []string {
	if rep.Values == nil || rep.Applied != deploy.Download {
		return []string{}
	}
	return append([]string{}, rep.Values.Reset...)
}

func valuesKept(rep *deploy.Report) int {
	if rep.Values == nil || rep.Applied != deploy.Download {
		return -1
	}
	return len(rep.Values.Kept)
}

// deployError is the status and message a refused or failed deploy is
// answered with.
func deployError(err error) (int, string) {
	var de *deploy.DiagError
	var nd *deploy.NeedsDownloadError
	var ve *deploy.VerifyError
	var se *deploy.StartStateError
	switch {
	case errors.As(err, &se):
		return http.StatusConflict, se.Error()
	case errors.As(err, &de):
		var lines []string
		for _, d := range de.Diags {
			lines = append(lines, d.String())
		}
		return http.StatusUnprocessableEntity, "outside the Logix v1 subset:\n" + strings.Join(lines, "\n")
	case errors.As(err, &nd):
		return http.StatusUnprocessableEntity, err.Error() + ". Download the project to the controller (nautilus: Download Project to Logix Controller, or `naut logix deploy --download --yes`) — it stops the controller."
	case errors.As(err, &ve):
		return http.StatusBadGateway, err.Error() + ": " + strings.Join(ve.Diffs, "; ")
	default:
		return http.StatusBadGateway, err.Error()
	}
}

// errorEvent is the stream's answer to a refused or failed deploy. A
// download the controller refused for its mode says so in plain words,
// with needsProgramMode set (the editor offers the Program-mode download)
// and the SDK's own error, status lines and all, as detail.
func errorEvent(err error) map[string]any {
	code, msg := deployError(err)
	ev := map[string]any{"event": "error", "status": code, "error": msg}
	var se *deploy.StartStateError
	if errors.As(err, &se) {
		ev["needsProgramMode"] = true
		if se.Raw != nil {
			ev["detail"] = se.Raw.Error()
		}
	}
	return ev
}

// deployStream is POST /api/logix/deploy: an online edit or a download,
// its progress streamed as NDJSON.
func (p *programPlane) deployStream(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if code, msg := p.auth(r, server.PermDeploy); code != 0 {
		server.WriteRefusal(w, code, msg)
		return
	}
	var req struct {
		Mode        string `json:"mode"`
		Source      string `json:"source"`
		BaseHash    string `json:"baseHash"`
		ProgramMode bool   `json:"programMode"`
		ResetValues bool   `json:"resetValues"`
		Yes         bool   `json:"yes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Source == "" {
		writeJSON(w, http.StatusBadRequest, "body must be {\"mode\": \"online\"|\"download\", \"source\": \"...\"}")
		return
	}
	switch req.Mode {
	case "online":
	case "download":
		if p.cfg.Download == nil {
			writeJSON(w, http.StatusNotImplemented, "this server does not download; run `naut logix deploy --download --yes`")
			return
		}
		if !req.Yes {
			writeJSON(w, http.StatusBadRequest, "a download STOPS the controller (and, with \"resetValues\", resets its tags to project values); send \"yes\": true when you mean it")
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, "mode must be \"online\" or \"download\"")
		return
	}
	if !p.begin(w, req.BaseHash) {
		return
	}
	// The plane is let go of when the report is out, before a warm-up.
	var endOnce sync.Once
	end := func() { endOnce.Do(p.end) }
	defer end()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	// The headers now, not with the first line: a warm edit's first line
	// can be seconds away, and its report the first thing it says.
	_ = rc.Flush()
	var wmu sync.Mutex
	open := true
	emit := func(v any) {
		wmu.Lock()
		defer wmu.Unlock()
		if !open {
			return
		}
		_ = json.NewEncoder(w).Encode(v)
		_ = rc.Flush()
	}
	progress := func(msg string) { emit(map[string]string{"event": "progress", "message": msg}) }
	defer func() {
		wmu.Lock()
		open = false
		wmu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(r.Context(), p.timeout())
	defer cancel()
	var rep *deploy.Report
	var err error
	if req.Mode == "download" {
		progress("starting a download to " + p.where())
		rep, err = p.cfg.Download(ctx, req.Source, DownloadOptions{ProgramMode: req.ProgramMode, ResetValues: req.ResetValues}, progress)
	} else {
		progress("starting an online edit on " + p.where())
		rep, err = p.cfg.Deploy(ctx, req.Source, progress)
	}
	if err != nil {
		emit(errorEvent(err))
		return
	}
	body := p.applied(req.Source, rep, started)
	body["event"] = "done"
	emit(body)
	if req.Mode != "download" || p.cfg.Warm == nil {
		return
	}
	// The report is out and the plane free; the warm-up is the next
	// edit's. Like the deploy, it stops if the request goes away (a
	// stopping serve), and the next edit then opens the session itself.
	end()
	p.cfg.Warm(ctx, progress)
}

// where names the controller a deploy goes to, for its first line.
func (p *programPlane) where() string {
	if p.cfg.CommPath != "" {
		return p.cfg.CommPath
	}
	if p.cfg.Controller != "" {
		return p.cfg.Controller
	}
	return "the controller"
}

func writeAny(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
