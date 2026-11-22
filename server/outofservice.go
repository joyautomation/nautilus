package server

// Equipment out of service (#715 PR 5):
//
//	GET  /api/alarms/out-of-service   every window, the soonest to end first
//	POST /api/alarms/out-of-service   {"equipment", "until"|"seconds", "reason", "by"}
//	POST /api/alarms/in-service       {"equipment", "by"}
//
// Out of service hides every alarm of a piece of equipment (alarm.Def.
// Equipment) until a deadline, a week at most, with a reason shown on
// each. It needs the out-of-service permission over EVERY covered alarm's
// tag (one action hides each, so an area-limited principal never hides
// another area's alarm) and a fresh password. Back in service needs only
// ack over one of them: showing alarms again must be easy. In a redundant pair
// both go through the operator-intent log, as a shelve does.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/oplog"
)

// oosRequest is POST /api/alarms/out-of-service. Until is an RFC3339
// timestamp or epoch milliseconds; Seconds is "for how long", resolved
// against the server's clock. One or the other.
type oosRequest struct {
	Equipment string  `json:"equipment"`
	Until     string  `json:"until"`
	Seconds   float64 `json:"seconds"`
	Reason    string  `json:"reason"`
	By        string  `json:"by"`
}

type inServiceRequest struct {
	Equipment string `json:"equipment"`
	By        string `json:"by"`
}

func (s *Server) handleOutOfServiceList(w http.ResponseWriter, r *http.Request) {
	if !s.alarmsOK(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.alarms.OutOfServiceList())
}

// authorizeEquipment authorizes perm over every alarm of equipment eq
// (anyOf: over at least one of them, for putting it back in service: a
// person who may acknowledge one of its alarms may show them all). An
// equipment no alarm belongs to is a 404 (after the permission check with
// eq as its own target, as an unknown alarm id is).
//
// For anyOf it also says which alarms the principal holds perm over and
// which not (the answer and the audit entry carry both, so a person who
// showed another area's alarms is visible).
func (s *Server) authorizeEquipment(w http.ResponseWriter, r *http.Request, perm, eq string, anyOf bool) (may, mayNot []string, ok bool) {
	tags, err := s.alarms.EquipmentTags(eq)
	if err != nil {
		if code, msg := s.authorize(r, perm, eq); code != 0 {
			http.Error(w, msg, code)
			return nil, nil, false
		}
		http.Error(w, err.Error(), alarmErrStatus(err))
		return nil, nil, false
	}
	if anyOf {
		code, msg := 0, ""
		key := ir.NameKey(eq)
		for _, rec := range s.alarms.Records() {
			if ir.NameKey(rec.Equipment) != key {
				continue
			}
			c, m := s.authorize(r, perm, rec.Tag)
			if c == 0 {
				may = append(may, rec.ID)
				continue
			}
			mayNot = append(mayNot, rec.ID)
			if code == 0 {
				code, msg = c, m
			}
		}
		if len(may) == 0 {
			http.Error(w, msg, code)
			return nil, nil, false
		}
		return may, mayNot, true
	}
	if code, msg := s.authorizeEach(r, perm, tags); code != 0 {
		http.Error(w, msg, code)
		return nil, nil, false
	}
	return nil, nil, true
}

func (s *Server) handleOutOfService(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if !s.alarmsOK(w) {
		return
	}
	var req oosRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Equipment == "" {
		http.Error(w, `expected {"equipment": "...", "until"|"seconds": ..., "reason": "..."}`, http.StatusBadRequest)
		return
	}
	if _, _, ok := s.authorizeEquipment(w, r, PermOOS, req.Equipment, false); !ok {
		return
	}
	var until time.Time
	switch {
	case req.Until != "":
		t, err := parseTime(req.Until, time.Time{})
		if err != nil {
			http.Error(w, "until: "+err.Error(), http.StatusBadRequest)
			return
		}
		until = t
	case req.Seconds > 0:
		until = time.Now().Add(time.Duration(req.Seconds * float64(time.Second)))
	default:
		http.Error(w, `out of service needs "until" (RFC3339 or epoch ms) or "seconds": there is no permanent out of service`,
			http.StatusBadRequest)
		return
	}
	by := s.recordBy(r, req.By)
	if s.alarmLog != nil {
		if s.refuseAlarmBeforeRestore(w) {
			return
		}
		if err := s.alarms.CheckOutOfService(req.Equipment, until, req.Reason); err != nil {
			http.Error(w, err.Error(), alarmErrStatus(err))
			return
		}
		s.oosThroughLog(w, r, oplog.Entry{Kind: oplog.KindOutOfService, ID: req.Equipment, Until: until.UnixMilli(), Reason: req.Reason, By: by})
		return
	}
	if err := s.alarms.TakeOutOfService(req.Equipment, until, by, req.Reason); err != nil {
		http.Error(w, err.Error(), alarmErrStatus(err))
		return
	}
	s.writeOutOfService(w, req.Equipment)
}

func (s *Server) handleInService(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if !s.alarmsOK(w) {
		return
	}
	var req inServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Equipment == "" {
		http.Error(w, `expected {"equipment": "..."}`, http.StatusBadRequest)
		return
	}
	may, mayNot, ok := s.authorizeEquipment(w, r, PermAck, req.Equipment, true)
	if !ok {
		return
	}
	// The answer (and so the audit entry) says which of its alarms the
	// person may acknowledge and which they showed without that right.
	w = ackScope{w, may, mayNot}
	by := s.recordBy(r, req.By)
	if s.alarmLog != nil {
		if s.refuseAlarmBeforeRestore(w) {
			return
		}
		if err := s.alarms.CheckInService(req.Equipment); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.oosThroughLog(w, r, oplog.Entry{Kind: oplog.KindInService, ID: req.Equipment, By: by})
		return
	}
	if err := s.alarms.ReturnToService(req.Equipment, by); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeOutOfService(w, req.Equipment)
}

// oosThroughLog proposes an out-of-service or in-service entry, checked
// already against the leader's engine.
func (s *Server) oosThroughLog(w http.ResponseWriter, r *http.Request, e oplog.Entry) {
	if s.refuseAlarmWithoutMajority(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteProposeWait)
	defer cancel()
	if _, err := s.proposeFor(ctx, r, e, true, s.alarmLog.node.Propose); err != nil {
		unknownOutcome(w, err)
		return
	}
	s.writeOutOfService(w, e.ID)
}

// oosAnswer is the answer to an out-of-service or in-service request: the
// equipment's window now (none once back in service) and its alarms as
// they stand.
type oosAnswer struct {
	Equipment    string   `json:"equipment"`
	OutOfService bool     `json:"outOfService"`
	UntilMs      int64    `json:"untilMs,omitempty"`
	By           string   `json:"by,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Alarms       []string `json:"alarms"`
	// MayAck and MayNotAck, on an in-service answer, are the alarms of the
	// equipment the person may acknowledge and those they may not (back
	// in service needs ack over one of them).
	MayAck    []string `json:"mayAck,omitempty"`
	MayNotAck []string `json:"mayNotAck,omitempty"`
}

// ackScope carries an in-service request's per-alarm ack scope to its
// answer.
type ackScope struct {
	http.ResponseWriter
	may, mayNot []string
}

func (s *Server) writeOutOfService(w http.ResponseWriter, eq string) {
	a := oosAnswer{Equipment: eq, Alarms: []string{}}
	if sc, ok := w.(ackScope); ok {
		a.MayAck, a.MayNotAck = sc.may, sc.mayNot
	}
	key := ir.NameKey(eq)
	for _, o := range s.alarms.OutOfServiceList() {
		if ir.NameKey(o.Equipment) == key {
			a.OutOfService, a.UntilMs, a.By, a.Reason, a.Alarms = true, o.UntilMs, o.By, o.Reason, o.Alarms
		}
	}
	if !a.OutOfService {
		for _, rec := range s.alarms.Records() {
			if ir.NameKey(rec.Equipment) == key {
				a.Alarms = append(a.Alarms, rec.ID)
			}
		}
	}
	writeJSON(w, http.StatusOK, a)
}
