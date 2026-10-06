package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/joyautomation/nautilus/runtime"
)

// Force and SFC endpoints — the PLC force table and Codesys-style online
// chart commands:
//
//	GET    /api/forces          the force table: {"forces": [{name, value, actual?, sinceMs}]}
//	POST   /api/forces          {"name": "StartPB", "value": true} — force a tag
//	                            or a struct member ("P101.Speed"); forcing an
//	                            address already forced changes its value
//	DELETE /api/forces/{name}   remove one force (404 when it was not forced)
//	POST   /api/forces/clear    remove every force: {"removed": n}
//	GET    /api/sfc             the running charts, steps (active?) and
//	                            transitions (enabled?)
//	POST   /api/sfc/step        {"step": "Fill", "pou"?: "Batch"} — jump the
//	                            chart to that step, once
//	POST   /api/sfc/transition  {"transition": "Start" | "t42", "pou"?: …} —
//	                            fire that transition once
//
// Every mutating call is a write: it passes authorizeWrite exactly like
// POST /api/tags (same-origin by default, the token when one is set), and it
// leaves an audit line in the controller's log naming what was done and
// from where. On a standby these are proxied to the leader like every other
// /api/ call — the force table is the ACTIVE controller's, and a takeover
// drops it (see runtime/force.go).
//
// The stream carries the table too: every Frame has `forces` (address →
// forced value) whenever at least one force is active, and omits it when
// none is — never delta-gated, so "absent" always means "nothing forced".

type forceRequest struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

type forcesResponse struct {
	Forces []runtime.Force `json:"forces"`
}

// refuseForce answers when this server cannot hold a force at all: one that
// fronts a controller it does not run (`naut logix serve`), where the
// device, not this store, owns the value.
func (s *Server) refuseForce(w http.ResponseWriter) bool {
	if s.tagWriter != nil {
		http.Error(w, "this server mirrors a controller it does not run — force on that controller", http.StatusNotImplemented)
		return true
	}
	return false
}

func (s *Server) handleForces(w http.ResponseWriter, r *http.Request) {
	fs := s.rt.Tags().Forces()
	if fs == nil {
		fs = []runtime.Force{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(forcesResponse{Forces: fs})
}

func (s *Server) handleForce(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if s.refuseForce(w) {
		return
	}
	var req forceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Value == nil {
		http.Error(w, `expected {"name": ..., "value": ...}`, http.StatusBadRequest)
		return
	}
	if err := s.rt.Tags().Force(req.Name, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	audit(r, "force: set", "tag", req.Name, "value", req.Value)
	s.handleForces(w, r)
}

func (s *Server) handleUnforce(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if s.refuseForce(w) {
		return
	}
	name := r.PathValue("name")
	if !s.rt.Tags().Unforce(name) {
		http.Error(w, fmt.Sprintf("%s is not forced", name), http.StatusNotFound)
		return
	}
	audit(r, "force: removed", "tag", name)
	s.handleForces(w, r)
}

func (s *Server) handleUnforceAll(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if s.refuseForce(w) {
		return
	}
	n := s.rt.Tags().UnforceAll()
	audit(r, "force: cleared all", "removed", n)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"removed": n})
}

type sfcResponse struct {
	Charts []runtime.SFCInfo `json:"charts"`
}

func (s *Server) handleSFC(w http.ResponseWriter, r *http.Request) {
	charts := s.rt.SFCCharts()
	if charts == nil {
		charts = []runtime.SFCInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sfcResponse{Charts: charts})
}

type sfcCommand struct {
	POU        string `json:"pou"`
	Step       string `json:"step"`
	Transition string `json:"transition"`
}

func (s *Server) handleSFCStep(w http.ResponseWriter, r *http.Request) {
	s.sfcCommand(w, r, true)
}

func (s *Server) handleSFCTransition(w http.ResponseWriter, r *http.Request) {
	s.sfcCommand(w, r, false)
}

func (s *Server) sfcCommand(w http.ResponseWriter, r *http.Request, step bool) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if s.refuseForce(w) {
		return
	}
	var req sfcCommand
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		(step && req.Step == "") || (!step && req.Transition == "") {
		if step {
			http.Error(w, `expected {"step": ..., "pou"?: ...}`, http.StatusBadRequest)
		} else {
			http.Error(w, `expected {"transition": ..., "pou"?: ...}`, http.StatusBadRequest)
		}
		return
	}
	var p *runtime.Program
	var err error
	if step {
		p, err = s.rt.SFCProgram(req.POU, req.Step, "")
	} else {
		p, err = s.rt.SFCProgram(req.POU, "", req.Transition)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var did string
	if step {
		did, err = p.SetStep(req.Step)
	} else {
		did, err = p.FireTransition(req.Transition)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if step {
		audit(r, "sfc: set active step", "pou", p.POU(), "step", did)
	} else {
		audit(r, "sfc: fired transition", "pou", p.POU(), "transition", did)
	}
	info, _ := p.SFC()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// audit leaves one log line per operator action that changes what the
// controller does outside its own logic — who (remote address) and what.
func audit(r *http.Request, msg string, args ...any) {
	slog.Info(msg, append(args, "remote", r.RemoteAddr)...)
}
