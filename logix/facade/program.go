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
// command that does it. A download stops a controller and is never one
// click away.

// ProgramPlane is the deployable program behind the program endpoints.
type ProgramPlane struct {
	// Source is the ladder program the controller runs now — the repo's
	// file at startup. Its hash is what the editor's baseHash must match.
	Source string
	// Deploy runs an online edit of src and reports. Errors of type
	// *deploy.DiagError, *deploy.NeedsDownloadError and
	// *deploy.VerifyError are answered with their own status codes.
	Deploy func(ctx context.Context, src string) (*deploy.Report, error)
	// Timeout bounds one deploy (default 15 minutes).
	Timeout time.Duration
}

type programPlane struct {
	cfg    ProgramPlane
	auth   func(r *http.Request) (int, string)
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
}

func (p *programPlane) get(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	info := programInfo{
		Task: "main", POU: runtime.POUOf(p.source), Source: p.source, Language: "ld",
		Hash: sourceHash(p.source), Editable: true,
	}
	if !p.lastAt.IsZero() {
		info.CompiledAt = p.lastAt.UnixMilli()
	}
	if p.busy {
		info.Error = "a deploy is in progress"
	}
	writeAny(w, http.StatusOK, info)
}

func (p *programPlane) put(w http.ResponseWriter, r *http.Request) {
	if code, msg := p.auth(r); code != 0 {
		writeJSON(w, code, msg)
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
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		writeJSON(w, http.StatusConflict, "a deploy is already in progress on this controller")
		return
	}
	if req.BaseHash != "" && req.BaseHash != sourceHash(p.source) {
		cur := sourceHash(p.source)
		p.mu.Unlock()
		writeAny(w, http.StatusConflict, map[string]string{
			"error": "controller program changed since your base — refresh and re-apply", "hash": cur, "task": "main",
		})
		return
	}
	p.busy = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.busy = false
		p.mu.Unlock()
	}()

	timeout := p.cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	rep, err := p.cfg.Deploy(ctx, req.Source)
	if err != nil {
		var de *deploy.DiagError
		var nd *deploy.NeedsDownloadError
		var ve *deploy.VerifyError
		switch {
		case errors.As(err, &de):
			var lines []string
			for _, d := range de.Diags {
				lines = append(lines, d.String())
			}
			writeJSON(w, http.StatusUnprocessableEntity, "outside the Logix v1 subset:\n"+strings.Join(lines, "\n"))
		case errors.As(err, &nd):
			writeJSON(w, http.StatusUnprocessableEntity, err.Error()+". Run `naut logix deploy --download --yes` — it stops the controller, so it is never one click away.")
		case errors.As(err, &ve):
			writeJSON(w, http.StatusBadGateway, err.Error()+": "+strings.Join(ve.Diffs, "; "))
		default:
			writeJSON(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	p.mu.Lock()
	p.source = req.Source
	p.last = rep
	p.lastAt = time.Now()
	hash := sourceHash(p.source)
	p.mu.Unlock()
	writeAny(w, http.StatusOK, map[string]any{
		"hash": hash, "task": "main", "applied": rep.Applied.String(), "same": rep.Same,
		"replaced": rep.Replaced, "verified": rep.Verified, "mode": rep.ModeAfter,
		"elapsedMs": rep.Elapsed.Milliseconds(), "resets": []string{},
	})
}

func writeAny(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
