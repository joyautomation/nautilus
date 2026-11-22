package server

import (
	"encoding/json"
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
// same front door as POST /api/tags (runtime.Tags.WriteAs): a force, a
// read-only tag and a tag's min/max refuse it exactly as they refuse a
// single write, and there is no way past them here. Only a parameter (a
// scalar setpoint with a literal init:) is named; anything else is refused.
// Any refusal stops the whole set: nothing is written. A standby does not
// import (a request reaching one is forwarded to the leader, and refused
// here if it is not one).
//
// Each value lands attributed to the importing principal, so the write-back
// of #718 slice 2 commits it as theirs. One journal entry (param.import)
// records every name with its old and new value.

// paramWrite applies the checked set; a var so a test can make one write fail
// after validation.
var paramWrite = func(t *runtime.Tags, ops []runtime.WriteOp, by string) []error {
	return t.WriteAs(ops, by)
}

// paramImportMax bounds one set.
const paramImportMax = 10000

type paramImportRequest struct {
	Version    int            `json:"version"`
	Parameters map[string]any `json:"parameters"`
	DryRun     bool           `json:"dryRun"`
}

// ParamImportResult is one parameter's outcome in the answer.
type ParamImportResult struct {
	Name   string `json:"name"`
	From   any    `json:"from,omitempty"`
	To     any    `json:"to,omitempty"`
	Status string `json:"status"` // change | unchanged | refused
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
	refused := false
	refuse := func(name, why string) {
		answer.Results = append(answer.Results, ParamImportResult{Name: name, Status: "refused", Reason: why})
		refused = true
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
		if runtime.ParamValuesEqual(p.Value, want) {
			answer.Results = append(answer.Results, ParamImportResult{Name: p.Name, From: p.Value, To: want, Status: "unchanged"})
			continue
		}
		answer.Results = append(answer.Results, ParamImportResult{Name: p.Name, From: p.Value, To: want, Status: "change"})
		ops = append(ops, runtime.WriteOp{Path: p.Name, Value: want})
		opAt = append(opAt, len(answer.Results)-1)
	}
	for i, err := range s.rt.Tags().CheckWrites(ops) {
		if err != nil {
			res := &answer.Results[opAt[i]]
			res.Status, res.Reason = "refused", err.Error()
			refused = true
		}
	}
	if refused {
		w.Header().Set(outcomeHeader, "refused")
		writeJSON(w, http.StatusUnprocessableEntity, answer)
		return
	}
	if req.DryRun || len(ops) == 0 {
		writeJSON(w, http.StatusOK, answer)
		return
	}
	for i, err := range paramWrite(s.rt.Tags(), ops, s.principalOf(r)) {
		if err != nil {
			// Only a race with a force or a range change lands here: the
			// set was whole when checked. Each result says whether it is in.
			res := &answer.Results[opAt[i]]
			res.Status, res.Reason = "failed", err.Error()
			refused = true
		}
	}
	for i := range answer.Results {
		if answer.Results[i].Status == "change" {
			answer.Results[i].Status = "applied"
		}
	}
	if refused {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "the set was only partly written: the results marked applied are in the controller, the ones marked failed are not", "answer": answer})
		return
	}
	answer.Applied = true
	writeJSON(w, http.StatusOK, answer)
}

// paramImportTarget is the journal entry's view of an import: the names it
// carries, each with the live value before and the value asked for.
func (s *Server) paramImportTarget(_ *http.Request, body []byte) auditTarget {
	var req paramImportRequest
	decodeBody(body, &req)
	before := map[string]any{}
	for n := range req.Parameters {
		before[n] = s.valueOf(n)
	}
	return auditTarget{
		target: fmt.Sprintf("%d parameters", len(req.Parameters)),
		before: before, after: req.Parameters,
		detail: map[string]any{"dryRun": req.DryRun},
		// What the controller holds now: only the values that landed.
		answered: func(b []byte) any {
			var a ParamImportAnswer
			if json.Unmarshal(b, &a) != nil || !a.Applied {
				return nil
			}
			return paramImportWritten(a.Results)
		},
		// A set that was only partly written says which values are in and
		// which are not.
		failed: func(b []byte) map[string]any {
			var f struct {
				Answer *ParamImportAnswer `json:"answer"`
			}
			if json.Unmarshal(b, &f) != nil || f.Answer == nil {
				return nil
			}
			notWritten := map[string]string{}
			for _, r := range f.Answer.Results {
				if r.Status == "failed" {
					notWritten[r.Name] = r.Reason
				}
			}
			return map[string]any{"written": paramImportWritten(f.Answer.Results), "notWritten": notWritten}
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
