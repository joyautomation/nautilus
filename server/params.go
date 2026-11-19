package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// IO-Link device parameters (#705, docs/design/iolink.md §7): the
// parameters of the device on a port are read and written through its
// master by ISDU. These routes serve them, and nothing here knows how a
// master does it: the project wires a ParamHook over its port devices.
//
//	GET /api/iolink/{port}/params          the device's parameters, from its IODD
//	GET /api/iolink/{port}/params/{ref}    one parameter's value, in any identity
//	                                       state, saying which device answered
//	PUT /api/iolink/{port}/params/{ref}    {"value": …} → what the device holds
//
// A write needs the config permission (PermConfig, controller-wide: a
// parameter is a change to a device), is journalled (param.write, accepted
// or refused), and is refused while the device on the port is not the one
// expected (409), as a parameter written to the wrong device is dangerous.
// A standby does not write.

// paramTimeout bounds one parameter call, the master's own timeout inside it.
const paramTimeout = 10 * time.Second

// The kinds of answer a ParamHook's errors wrap, for the status each gets;
// anything else is the master's or the device's own failure (502).
var (
	// ErrParamUnknown: no such port device, or no such parameter (404).
	ErrParamUnknown = errors.New("unknown port device or parameter")
	// ErrParamInvalid: not allowed by the device's IODD (422).
	ErrParamInvalid = errors.New("not allowed by the IODD")
	// ErrParamRefused: the port cannot take it now (409).
	ErrParamRefused = errors.New("refused")
	// ErrParamUncertain: the device on the port changed while a write was
	// under way, so whether the right device took it is not known (502, and
	// the journal records the outcome as unknown).
	ErrParamUncertain = errors.New("outcome uncertain")
)

// ParamHook is what the parameter routes need of the project.
type ParamHook struct {
	// List is a port device's parameters from its IODD; ok is false for a
	// port device the project does not have.
	List func(port string) (ParamList, bool)
	// Read reads one parameter of a port device; ref is a variable id, name
	// or index, a record's item after a dot.
	Read func(ctx context.Context, port, ref string) (ParamAnswer, error)
	// Write writes one, and answers what the device then holds. value is a
	// bool, an int64 or float64 (a JSON number), or a string.
	Write func(ctx context.Context, port, ref string, value any) (ParamAnswer, error)
}

// ParamList is GET /api/iolink/{port}/params.
type ParamList struct {
	Port   string      `json:"port"`
	Master string      `json:"master"`
	Params []ParamInfo `json:"params"`
}

// ParamInfo is one parameter of a port device's IODD.
type ParamInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Index   int      `json:"index"`
	Ref     string   `json:"ref"`
	Type    string   `json:"type,omitempty"`
	Access  string   `json:"access"`
	Octets  int      `json:"octets,omitempty"`
	Range   []string `json:"range,omitempty"`
	Default string   `json:"default,omitempty"`
	// Carried says the port's master can carry it in one call.
	Carried bool `json:"carried"`
}

// ParamAnswer is a parameter as read or as written, with the port's
// identity when it was.
type ParamAnswer struct {
	Port     string `json:"port"`
	Variable string `json:"variable"`
	Index    int    `json:"index"`
	Subindex int    `json:"subindex"`
	Value    any    `json:"value"`
	// Raw is the octets on the wire, as hex.
	Raw string `json:"raw"`
	// Identity is the identity check (Ok, NoDevice, WrongDevice,
	// Unchecked), and IdentityChecked whether the master's ids had arrived:
	// a read taken from the wrong device says so.
	Identity        string `json:"identity"`
	IdentityChecked bool   `json:"identityChecked"`
}

func (s *Server) paramsUnavailable(w http.ResponseWriter) bool {
	if s.params == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this controller has no IO-Link port devices"})
		return true
	}
	return false
}

// paramStatus is the status a hook's error answers with.
func paramStatus(err error) int {
	switch {
	case errors.Is(err, ErrParamUnknown):
		return http.StatusNotFound
	case errors.Is(err, ErrParamInvalid):
		return http.StatusUnprocessableEntity
	case errors.Is(err, ErrParamRefused):
		return http.StatusConflict
	}
	return http.StatusBadGateway
}

func (s *Server) handleParamList(w http.ResponseWriter, r *http.Request) {
	if s.paramsUnavailable(w) {
		return
	}
	list, ok := s.params.List(r.PathValue("port"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no port device %q", r.PathValue("port"))})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleParamRead(w http.ResponseWriter, r *http.Request) {
	if s.paramsUnavailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), paramTimeout)
	defer cancel()
	ans, err := s.params.Read(ctx, r.PathValue("port"), r.PathValue("ref"))
	if err != nil {
		writeJSON(w, paramStatus(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ans)
}

type paramWriteRequest struct {
	Value any `json:"value"`
}

// paramValue is a JSON value as the codec takes it: an integer as int64
// (uint64 past it), a fraction as float64, text and booleans as they are.
func paramValue(v any) (any, bool) {
	switch x := v.(type) {
	case bool, string:
		return x, true
	case json.Number:
		if n, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return n, true
		}
		if n, err := strconv.ParseUint(string(x), 10, 64); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(string(x), 64); err == nil {
			return f, true
		}
	}
	return nil, false
}

func decodeParamBody(body []byte) (any, bool) {
	var req paramWriteRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil || req.Value == nil {
		return nil, false
	}
	return paramValue(req.Value)
}

func (s *Server) handleParamWrite(w http.ResponseWriter, r *http.Request) {
	port := r.PathValue("port")
	if code, msg := s.authorize(r, PermConfig, port); code != 0 {
		refuse(w, msg, code)
		return
	}
	if s.params == nil {
		refuse(w, "this controller has no IO-Link port devices", http.StatusNotFound)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxWriteBody))
	value, ok := decodeParamBody(body)
	if !ok {
		refuse(w, `expected {"value": a number, text or true/false}`, http.StatusBadRequest)
		return
	}
	if s.refuseNotLeading(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), paramTimeout)
	defer cancel()
	ans, err := s.params.Write(ctx, port, r.PathValue("ref"), value)
	if err != nil {
		code := paramStatus(err)
		switch {
		case errors.Is(err, ErrParamUncertain):
			w.Header().Set(outcomeHeader, "unknown")
		case code != http.StatusBadGateway:
			w.Header().Set(outcomeHeader, "refused")
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ans)
}

// paramTarget is what a parameter write acts on: the port device and the
// parameter as asked for, the value the device held just before (an ISDU
// read, which a write-only parameter or a failed read leaves out), and
// what was asked for; after is what the device then held.
func (s *Server) paramTarget(r *http.Request, body []byte) auditTarget {
	port, ref := r.PathValue("port"), r.PathValue("ref")
	t := auditTarget{target: port + "/" + ref}
	if v, ok := decodeParamBody(body); ok {
		t.after = v
	}
	t.answered = func(b []byte) any {
		var ans ParamAnswer
		if json.Unmarshal(b, &ans) == nil {
			return ans.Value
		}
		return nil
	}
	if s.params == nil {
		return t
	}
	ctx, cancel := context.WithTimeout(r.Context(), paramTimeout)
	defer cancel()
	if ans, err := s.params.Read(ctx, port, ref); err == nil {
		t.before = ans.Value
		t.detail = map[string]any{"variable": ans.Variable, "index": ans.Index, "subindex": ans.Subindex, "identity": ans.Identity}
	}
	return t
}
