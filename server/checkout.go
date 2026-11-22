package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
)

// The I/O checkout (#719):
//
//	GET  /api/checkout                    the sheet, the open run, every run
//	GET  /api/checkout/runs/{id}          a run: its latest result for each point, and progress by device and area
//	POST /api/checkout/runs               {rehearsal?}: begin a run (one is open at a time)
//	POST /api/checkout/runs/{id}/results  {point, outcome: pass|fail|note, note?}: record a result
//	POST /api/checkout/runs/{id}/abandon  {reason}: end a run without sealing it
//
// A result is a person's: a token, which names nobody, records none. The
// controller writes what it read at the point's tag at that instant, and its
// quality, into the result: the client cannot say what was seen. Recording
// needs the checkout permission in the point's area; beginning and
// abandoning a run need it for the whole controller. Outputs are not forced
// here yet (the next slice), and a redundant pair refuses the whole surface
// until its results travel through the replicated log.

// CheckoutOptions turn the checkout on.
type CheckoutOptions struct {
	// Store keeps the runs, Sheet lists the points, Signoff is the rule
	// ("two-person" or "single") a sealed run will carry.
	Store   *checkout.Store
	Sheet   *checkout.Sheet
	Signoff string
}

// checkoutOff answers when the checkout is not available here.
func (s *Server) checkoutOff(w http.ResponseWriter) bool {
	switch {
	case s.checkout == nil || s.checkout.Sheet == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the I/O checkout is not enabled for this project: add a checkout: section to the manifest (naut checkout init writes the sheet), with an auth: block with users and a state directory to keep the results in (auth.state-dir, or systemd StateDirectory=)"})
	case s.cluster != nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the I/O checkout is not available on a redundant pair yet: its results would be kept on one controller only"})
	case s.checkout.Store == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the I/O checkout has nowhere to keep its results: it needs an auth: block with users and a state directory (auth.state-dir, or systemd StateDirectory=)"})
	default:
		return false
	}
	return true
}

type checkoutPoint struct {
	checkout.Point
	// Result is the point's latest result in the open run.
	Result *checkout.Result `json:"result,omitempty"`
}

type checkoutView struct {
	Title   string           `json:"title,omitempty"`
	Hash    string           `json:"hash"`
	Signoff string           `json:"signoff"`
	Points  []checkout.Point `json:"points"`
	Run     *checkout.Run    `json:"run,omitempty"`
	Runs    []checkout.Run   `json:"runs"`
}

func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request) {
	if s.checkoutOff(w) {
		return
	}
	v := checkoutView{Title: s.checkout.Sheet.Title, Hash: s.checkout.Sheet.Hash(), Signoff: s.checkout.Signoff,
		Points: s.checkout.Sheet.Points, Runs: s.checkout.Store.List()}
	if run, ok := s.checkout.Store.Current(); ok {
		v.Run = &run
	}
	writeJSON(w, http.StatusOK, v)
}

type checkoutRunView struct {
	checkout.Run
	Latest   map[string]checkout.Result   `json:"latest"`
	ByDevice map[string]checkout.Progress `json:"byDevice"`
	ByArea   map[string]checkout.Progress `json:"byArea"`
}

func (s *Server) handleCheckoutRun(w http.ResponseWriter, r *http.Request) {
	if s.checkoutOff(w) {
		return
	}
	run, ok := s.checkout.Store.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such run"})
		return
	}
	latest := s.checkout.Store.Latest(run.ID)
	sh := s.checkout.Sheet
	writeJSON(w, http.StatusOK, checkoutRunView{Run: run, Latest: latest,
		ByDevice: sh.ProgressBy(func(p checkout.Point) string { return p.Device }, latest),
		ByArea:   sh.ProgressBy(func(p checkout.Point) string { return p.Area }, latest)})
}

// checkoutPerson is who records: a person, never a token (a token names
// nobody, and a checkout sheet is signed by names).
func (s *Server) checkoutPerson(r *http.Request) (auth.Principal, int, string) {
	a := s.actorOf(r)
	switch a.pr.Kind {
	case auth.KindUser, auth.KindOIDC:
		return a.pr, 0, ""
	}
	return a.pr, http.StatusUnauthorized, "sign in as a person to record a checkout: a checkout sheet is signed by names" + signInHint
}

type checkoutRequest struct {
	Rehearsal bool   `json:"rehearsal"`
	Point     string `json:"point"`
	Outcome   string `json:"outcome"`
	Note      string `json:"note"`
	Reason    string `json:"reason"`
}

func (s *Server) checkoutBegin(w http.ResponseWriter, r *http.Request) {
	s.checkoutWrite(w, r, "", func(pr auth.Principal, req checkoutRequest) (any, int, error) {
		run, err := s.checkout.Store.Begin(req.Rehearsal, s.checkout.Sheet.Hash(), s.checkout.Sheet.Title, noteAuthor(pr.String()), pr.String())
		return run, http.StatusCreated, err
	})
}

func (s *Server) checkoutResult(w http.ResponseWriter, r *http.Request) {
	s.checkoutWrite(w, r, "point", func(pr auth.Principal, req checkoutRequest) (any, int, error) {
		pt, ok := s.checkout.Sheet.Point(req.Point)
		if !ok {
			return nil, http.StatusUnprocessableEntity, errors.New("the sheet has no point " + req.Point)
		}
		quality := "good"
		if q, bad := qualityJSON(s.rt.Quality())[pt.Tag]; bad {
			quality = q
		}
		res, err := s.checkout.Store.Record(r.PathValue("id"), pt.ID, req.Outcome, req.Note, s.valueOf(pt.Tag), quality, noteAuthor(pr.String()), pr.String())
		return res, http.StatusCreated, err
	})
}

func (s *Server) checkoutAbandon(w http.ResponseWriter, r *http.Request) {
	s.checkoutWrite(w, r, "", func(pr auth.Principal, req checkoutRequest) (any, int, error) {
		run, err := s.checkout.Store.Abandon(r.PathValue("id"), req.Reason, noteAuthor(pr.String()), pr.String())
		return run, http.StatusOK, err
	})
}

// checkoutWrite is the shared front of the three writes: the surface is on,
// the request is a same-origin or token write, a person is signed in, the body
// parses, the permission is held for the point's tag (scopeBy "point") or the
// whole controller, and then do runs and answers.
func (s *Server) checkoutWrite(w http.ResponseWriter, r *http.Request, scopeBy string, do func(auth.Principal, checkoutRequest) (any, int, error)) {
	if s.checkoutOff(w) {
		return
	}
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	var req checkoutRequest
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) { // begin takes an empty body
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is JSON: " + err.Error()})
		return
	}
	scope := auth.All
	if scopeBy == "point" {
		pt, ok := s.checkout.Sheet.Point(req.Point)
		if !ok {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the sheet has no point " + req.Point})
			return
		}
		scope = pt.Tag
	}
	// The area first: a principal fenced to another area is told so (403)
	// whoever they are; only then is a person asked for.
	if code, msg := s.authorize(r, PermCheckout, scope); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	pr, code, msg := s.checkoutPerson(r)
	if code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	out, status, err := do(pr, req)
	switch {
	case err == nil:
		writeJSON(w, status, out)
	case errors.Is(err, checkout.ErrNoRun):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such run"})
	case errors.Is(err, checkout.ErrNotOpen), errors.Is(err, checkout.ErrRunOpen):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case status == http.StatusUnprocessableEntity:
		writeJSON(w, status, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}

// checkoutTarget is the audit target of a checkout write: the point's tag for
// a result, nothing (the whole controller) otherwise, with the run and point
// in the detail.
func (s *Server) checkoutTarget(r *http.Request, body []byte) auditTarget {
	var req checkoutRequest
	_ = json.NewDecoder(bytes.NewReader(body)).Decode(&req)
	t := auditTarget{target: auth.All, detail: map[string]any{}}
	if id := r.PathValue("id"); id != "" {
		t.detail["run"] = id
	}
	if req.Point != "" {
		t.detail["point"] = req.Point
		if s.checkout != nil && s.checkout.Sheet != nil {
			if pt, ok := s.checkout.Sheet.Point(req.Point); ok {
				t.target = pt.Tag
			}
		}
		t.after = map[string]any{"outcome": req.Outcome, "note": req.Note}
	}
	if req.Reason != "" {
		t.after = map[string]any{"reason": req.Reason}
	}
	if req.Rehearsal {
		t.detail["rehearsal"] = true
	}
	return t
}
