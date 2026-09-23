package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/joyautomation/nautilus/runtime"
)

// Program history — the git provenance of the running controller, served
// by the controller itself:
//
//	GET  /api/program/history   every commit that touched the project, with
//	                            its diff; plus the running programs' state
//	POST /api/program/activate  warm-swap every program to its source at a
//	                            given commit (gated like online edits)
//
// The history is captured where git exists — `naut build` embeds a
// snapshot in the artifact, `naut run` reads the working repo — so the
// deployed controller answers from data it carries, with no git binary, no
// .git dir, and no network. See Options.History / Options.SourcesAt.
//
// Activation is time travel for LOGIC only. A commit's program sources
// (libraries composed ahead, exactly as boot would) compile and warm-swap
// like any online edit: state migrates by name and type, a compile failure
// leaves everything running, and each program keeps its one-step rollback.
// What activation cannot do is change topology — tasks, tags, drivers, and
// server config stay as the running manifest booted them. A commit that
// changed those needs its image deployed, and the handler says so instead
// of half-applying it.

// ProgramHistory is a captured git history of a project: the commits that
// touched it (newest first) and, for activation, a content-addressed
// snapshot of the project's files at each commit. Blobs are keyed by git's
// own blob object id, so a file unchanged across fifty commits is stored
// once. Built internally (internal/vcs) at build/run time; the type is
// public so Go-tier callers can wire Options.History from their own capture.
type ProgramHistory struct {
	// Built is HEAD when the artifact was built — the commit the deployed
	// baseline corresponds to. BuiltDirty flags a build from a tree with
	// uncommitted changes: the artifact's provenance is then approximate,
	// and the response says so rather than implying a clean lineage.
	Built      string            `json:"built,omitempty"`
	BuiltDirty bool              `json:"builtDirty,omitempty"`
	Commits    []ProgramCommit   `json:"commits"`
	Blobs      map[string]string `json:"blobs,omitempty"`
}

// ProgramCommit is one commit in a ProgramHistory. Files maps each project
// file's path (as the manifest would load it) to its blob id in Blobs —
// the complete source snapshot at this commit, which is what makes the
// commit activatable rather than merely reviewable.
type ProgramCommit struct {
	Sha     string            `json:"sha"`
	Short   string            `json:"short"`
	Author  string            `json:"author"`
	Date    string            `json:"date"`
	Subject string            `json:"subject"`
	Diff    string            `json:"diff"`
	Files   map[string]string `json:"files,omitempty"`
}

// Find resolves a full or abbreviated sha to its commit, nil if absent.
func (h *ProgramHistory) Find(sha string) *ProgramCommit {
	if h == nil || sha == "" {
		return nil
	}
	for i := range h.Commits {
		c := &h.Commits[i]
		if c.Sha == sha || c.Short == sha || (len(sha) >= 7 && strings.HasPrefix(c.Sha, sha)) {
			return c
		}
	}
	return nil
}

// historyInfo is the GET /api/program/history response: the captured
// commits (diffs included — the review surface), the deployed baseline,
// and the running programs' current layer, so a client can see in one
// read both what CI shipped and how far online edits have drifted from it.
// Blobs and file snapshots stay server-side; activation happens by sha.
type historyInfo struct {
	Built      string `json:"built,omitempty"`
	BuiltDirty bool   `json:"builtDirty,omitempty"`
	// Active is the last activated commit, cleared by any PUT or rollback
	// — "" means the running source is as-built, or hand-edited past the
	// last activation. The programs' Dirty flags carry the drift signal.
	Active   string           `json:"active,omitempty"`
	Editable bool             `json:"editable"`
	Commits  []commitInfo     `json:"commits"`
	Programs []programSummary `json:"programs,omitempty"`
}

type commitInfo struct {
	Sha     string `json:"sha"`
	Short   string `json:"short"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	Diff    string `json:"diff"`
	// Activatable: this commit carries a full file snapshot to rebuild
	// sources from. False for a commit captured diff-only (files pruned by
	// size/encoding limits at capture time).
	Activatable bool `json:"activatable"`
}

func (s *Server) history() *ProgramHistory {
	if s.historyFn == nil {
		return nil
	}
	return s.historyFn()
}

func (s *Server) handleProgramHistory(w http.ResponseWriter, r *http.Request) {
	h := s.history()
	info := historyInfo{
		Editable: s.onlineEdits && s.sourcesAt != nil,
		Commits:  []commitInfo{}, // an empty history serves [], not null
		Active:   s.activeSha(),
	}
	if h != nil {
		info.Built, info.BuiltDirty = h.Built, h.BuiltDirty
		for _, c := range h.Commits {
			info.Commits = append(info.Commits, commitInfo{
				Sha: c.Sha, Short: c.Short, Author: c.Author,
				Date: c.Date, Subject: c.Subject, Diff: c.Diff,
				Activatable: len(c.Files) > 0,
			})
		}
	}
	names := append([]string{runtime.MainTaskName}, s.rt.TaskNames()...)
	for _, name := range names {
		p := s.rt.TaskProgram(name)
		st := p.Status()
		info.Programs = append(info.Programs, programSummary{
			Task: name, POU: p.POU(), Language: runtime.Language(p.Source()),
			Hash: p.Hash(), Dirty: p.Dirty(), CanRollback: p.CanRollback(),
			Scans: st.Scans, Error: st.Error,
		})
	}
	writeJSON(w, http.StatusOK, info)
}

// activateReport is one program's outcome in the POST /api/program/activate
// response — the same shape a single warm swap reports, per task.
type activateReport struct {
	Task string `json:"task"`
	runtime.SwapReport
}

// handleActivate warm-swaps every program in the resource to its source at
// one commit. All sources compile before any swap happens, so a commit
// whose logic no longer compiles against today's runtime is rejected whole
// — a controller is never left running half of two revisions.
func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeProgramEdit(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if s.sourcesAt == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "no program history on this controller — build with `naut build` inside a git repo, or run from one",
		})
		return
	}
	var req struct {
		Sha string `json:"sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Sha == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"sha\": \"<commit>\"}"})
		return
	}
	c := s.history().Find(req.Sha)
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no commit " + req.Sha + " in this controller's history"})
		return
	}
	sources, err := s.sourcesAt(c.Sha)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "cannot rebuild sources at " + c.Short + ": " + err.Error(),
		})
		return
	}

	// Topology must match by task name, both ways: a task the commit lacks
	// has nothing to run, and a task the commit adds has no scan to run on.
	// Either way the difference is a manifest change — deploy that commit's
	// image instead of activating it.
	running := append([]string{runtime.MainTaskName}, s.rt.TaskNames()...)
	for _, name := range running {
		if _, ok := sources[name]; !ok {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "commit " + c.Short + " has no program for task " + name + " — topology changed; deploy that commit instead",
			})
			return
		}
	}
	if len(sources) != len(running) {
		extra := []string{}
		for name := range sources {
			found := false
			for _, r := range running {
				if r == name {
					found = true
				}
			}
			if !found {
				extra = append(extra, name)
			}
		}
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "commit " + c.Short + " declares tasks this controller does not run (" +
				strings.Join(extra, ", ") + ") — topology changed; deploy that commit instead",
		})
		return
	}

	// Validate every source before touching any program.
	for _, name := range running {
		if _, err := runtime.Compile(sources[name]); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": "task " + name + " at " + c.Short + " does not compile: " + err.Error(),
			})
			return
		}
	}

	reports := make([]activateReport, 0, len(running))
	for _, name := range running {
		p := s.rt.TaskProgram(name)
		rep, err := p.SwapWarm(sources[name])
		if err != nil {
			// Pre-validated, so this is unreachable short of a racing edit;
			// report honestly rather than mask it.
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":    "task " + name + ": " + err.Error(),
				"programs": reports,
			})
			return
		}
		reports = append(reports, activateReport{Task: name, SwapReport: rep})
	}
	s.setActive(c.Sha)
	writeJSON(w, http.StatusOK, map[string]any{"sha": c.Sha, "short": c.Short, "programs": reports})
}

func (s *Server) activeSha() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// setActive records (or, with "", clears) which commit the running programs
// were last activated to. A PUT or rollback clears it: after either, the
// running source no longer corresponds to any one commit.
func (s *Server) setActive(sha string) {
	s.mu.Lock()
	s.active = sha
	s.mu.Unlock()
}
