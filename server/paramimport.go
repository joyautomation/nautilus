package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// POST /api/parameters/import applies a parameter set (what `naut params
// export` writes) to the live setpoints, all or nothing (#718 slice 4).
//
//	{"version": 1, "parameters": {"Tank.SP": 42.5}, "dryRun": true}
//
// It is a bulk write, so it asks for the config permission (controller-wide,
// with a fresh password for a browser sign-in) AND, for every tag in the set,
// the write authorization POST /api/tags applies (area-scoped under auth:
// areas); one tag that fails refuses the whole set with a 403 naming it.
// Every value goes through the
// same front door as POST /api/tags (runtime.Tags.WriteSet, the WriteAs rules: a force, a
// read-only tag and a tag's min/max refuse it exactly as they refuse a
// single write, and there is no way past them here. Only a parameter (a
// scalar setpoint with a literal init:) is named; anything else is refused.
// Any refusal stops the whole set: nothing is written. The check and the
// write are one step under the store's lock (Tags.WriteSet), so the set is
// never half in, and the answer's from/to are what the store held and holds. A standby does not
// import (a request reaching one is forwarded to the leader, and refused
// here if it is not one).
//
// Each value lands attributed to the importing principal, so the write-back
// of #718 slice 2 commits it as theirs. One journal entry (param.import)
// records every name with its old and new value.

// paramImportMax bounds one set.
//
// 500: one journal entry holds the old and new value of every name (it is
// capped at valueMax), and a controller's tuned setpoints are dozens to a
// few hundred. A bigger set is split.
const paramImportMax = 500

type paramImportRequest struct {
	Version    int            `json:"version"`
	Parameters map[string]any `json:"parameters"`
	DryRun     bool           `json:"dryRun"`
	// ExpectFrom, optional: name -> the value the caller last saw. If any
	// tag holds something else when the set is applied, the whole set is
	// refused (409) and nothing is written.
	ExpectFrom map[string]any `json:"expectFrom,omitempty"`
}

// ParamImportResult is one parameter's outcome in the answer.
type ParamImportResult struct {
	Name   string `json:"name"`
	From   any    `json:"from,omitempty"`
	To     any    `json:"to,omitempty"`
	Status string `json:"status"` // change (dry run) | applied | unchanged | refused | moved
	Reason string `json:"reason,omitempty"`
}

// ParamImportAnswer is the route's answer.
type ParamImportAnswer struct {
	DryRun  bool                `json:"dryRun"`
	Applied bool                `json:"applied"`
	Results []ParamImportResult `json:"results"`
}

func paramKind(v any) string {
	switch v.(type) {
	case bool:
		return "bool"
	case string:
		return "text"
	case nil:
		return "none"
	}
	return "number"
}

func (s *Server) handleParametersImport(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorize(r, PermConfig, ""); code != 0 {
		WriteRefusal(w, code, msg)
		return
	}
	if s.tagWriter != nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this server mirrors a controller it does not run: import on that controller"})
		return
	}
	if s.cluster != nil && !s.cluster.Status().IsLeader {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this controller is not leading: import on the leader (a request to a standby is forwarded to it)"})
		return
	}
	var req paramImportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"version": 1, "parameters": {"<name>": <value>}, "dryRun": false}: ` + err.Error()})
		return
	}
	if req.Version != 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("version %d is not supported: this controller reads version 1", req.Version)})
		return
	}
	if len(req.Parameters) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no parameters: an empty set changes nothing"})
		return
	}
	if len(req.Parameters) > paramImportMax {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("%d parameters: at most %d in one set", len(req.Parameters), paramImportMax)})
		return
	}
	live := map[string]runtime.Parameter{}
	for _, p := range s.rt.Parameters() {
		live[ir.NameKey(p.Name)] = p
	}
	expect := map[string]any{}
	for n, v := range req.ExpectFrom {
		if _, in := req.Parameters[n]; !in {
			if !hasKeyFold(req.Parameters, n) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("expectFrom names %q, which is not in parameters", n)})
				return
			}
		}
		switch v.(type) {
		case float64, bool, string:
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("expectFrom %q must be a number, a bool or text", n)})
			return
		}
		expect[ir.NameKey(n)] = v
	}
	// An import is many tag writes: each tag must pass the authorization
	// POST /api/tags applies (the write permission over that tag, by area).
	// Any tag that fails refuses the whole set, before anything else is
	// looked at, so a refusal never says more about a tag than the 403.
	var denied []string
	var deniedCode int
	var deniedMsg string
	for n := range req.Parameters {
		target := n
		if p, ok := live[ir.NameKey(n)]; ok {
			target = p.Name
		}
		if code, msg := s.authorize(r, PermWrite, target); code != 0 {
			denied = append(denied, target)
			deniedCode, deniedMsg = code, msg
		}
	}
	if len(denied) > 0 {
		sort.Strings(denied)
		w.Header().Set(outcomeHeader, "refused")
		if deniedCode == http.StatusUnauthorized {
			WriteRefusal(w, deniedCode, deniedMsg)
			return
		}
		writeJSON(w, http.StatusForbidden, map[string]any{"error": fmt.Sprintf("you may not write %s: %s. Nothing was written (an import writes only tags you could write one at a time)", strings.Join(denied, ", "), deniedMsg), "denied": denied})
		return
	}
	names := make([]string, 0, len(req.Parameters))
	for n := range req.Parameters {
		names = append(names, n)
	}
	sort.Strings(names)

	answer := ParamImportAnswer{DryRun: req.DryRun}
	var ops []runtime.WriteOp
	var opAt []int // index into answer.Results for each op
	refused, moved, otherRefusal := false, false, false
	refuse := func(name, why string) {
		answer.Results = append(answer.Results, ParamImportResult{Name: name, Status: "refused", Reason: why})
		refused, otherRefusal = true, true
	}
	seen := map[string]string{}
	for _, n := range names {
		want := req.Parameters[n]
		key := ir.NameKey(n)
		if prev, dup := seen[key]; dup {
			refuse(n, fmt.Sprintf("names the same parameter as %q (tag names ignore case)", prev))
			continue
		}
		seen[key] = n
		p, ok := live[key]
		if !ok {
			refuse(n, "not a parameter on this controller (a scalar setpoint with a literal init: in the manifest)")
			continue
		}
		switch want.(type) {
		case float64, bool, string:
		default:
			refuse(p.Name, fmt.Sprintf("a value must be a number, a bool or text, not %T", want))
			continue
		}
		if paramKind(p.Value) != paramKind(want) {
			refuse(p.Name, fmt.Sprintf("it holds a %s, the set has a %s", paramKind(p.Value), paramKind(want)))
			continue
		}
		answer.Results = append(answer.Results, ParamImportResult{Name: p.Name, Status: "change"})
		ops = append(ops, runtime.WriteOp{Path: p.Name, Value: want})
		opAt = append(opAt, len(answer.Results)-1)
	}
	// The one locked step: every op is checked (force, read-only, range, the
	// strict conversion to the tag's type) and, only if all pass and this is
	// not a dry run, written, so a set is never half in. From, To and
	// "unchanged" are read inside that lock, not from the earlier snapshot.
	outcomes := s.rt.Tags().WriteSetGuarded(ops, expect, s.principalOf(r), refused || req.DryRun)
	for i, o := range outcomes {
		res := &answer.Results[opAt[i]]
		res.From, res.To = o.From, o.To
		switch {
		case errors.Is(o.Err, runtime.ErrMoved):
			res.Status, res.Reason = "moved", o.Err.Error()
			refused, moved = true, true
		case o.Err != nil:
			res.Status, res.Reason = "refused", o.Err.Error()
			refused, otherRefusal = true, true
		case o.Unchanged:
			res.Status = "unchanged"
		}
	}
	if refused {
		w.Header().Set(outcomeHeader, "refused")
		// Only a moved value: 409, so a client knows to look again. Any
		// other refusal is the set's own fault and stays 422.
		code := http.StatusUnprocessableEntity
		if moved && !otherRefusal {
			code = http.StatusConflict
		}
		writeJSON(w, code, answer)
		return
	}
	if req.DryRun {
		writeJSON(w, http.StatusOK, answer)
		return
	}
	for i := range answer.Results {
		if answer.Results[i].Status == "change" {
			answer.Results[i].Status = "applied"
			answer.Applied = true // only when something was written
		}
	}
	writeJSON(w, http.StatusOK, answer)
}

// paramImportTarget is the journal entry's view of an import: the names it
// carries (at most paramImportMax, so a huge body costs the seam nothing),
// each with the live value before. After is what landed: only the values
// the import changed, as the store holds them; a refused or dry-run entry
// keeps the set that was asked for.
func (s *Server) paramImportTarget(_ *http.Request, body []byte) auditTarget {
	var req paramImportRequest
	decodeBody(body, &req)
	before := map[string]any{}
	if len(req.Parameters) <= paramImportMax {
		for n := range req.Parameters {
			before[n] = s.valueOf(n)
		}
	}
	return auditTarget{
		target: fmt.Sprintf("%d parameters", len(req.Parameters)),
		before: before, after: req.Parameters,
		detail: map[string]any{"dryRun": req.DryRun},
		answered: func(b []byte) any {
			var a ParamImportAnswer
			if json.Unmarshal(b, &a) != nil || !a.Applied {
				return req.Parameters
			}
			return paramImportWritten(a.Results)
		},
	}
}

// paramImportWritten is name -> value for the results that landed.
func paramImportWritten(rs []ParamImportResult) map[string]any {
	out := map[string]any{}
	for _, r := range rs {
		if r.Status == "applied" {
			out[r.Name] = r.To
		}
	}
	return out
}

// paramImportRefusal names what a refused import tried to do by parsing the
// body alone: how many parameters, and whether it was a dry run.
func paramImportRefusal(_ *http.Request, body []byte) auditTarget {
	var req paramImportRequest
	decodeBody(body, &req)
	return auditTarget{target: fmt.Sprintf("%d parameters", len(req.Parameters)), detail: map[string]any{"dryRun": req.DryRun}}
}

// hasKeyFold reports whether m has a key that names the same tag as n.
func hasKeyFold(m map[string]any, n string) bool {
	for k := range m {
		if ir.NameKey(k) == ir.NameKey(n) {
			return true
		}
	}
	return false
}
