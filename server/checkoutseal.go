//go:build !js

package server

import (
	"fmt"
	"net/http"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
)

// checkoutSeal signs off a run (#719 slice 4): the permission is signoff, which
// is elevated (a fresh password), so a stolen session cannot sign. Every
// condition is the store's, under its lock; what the controller derives about
// its own devices is decided here and recorded in the seal. The client says
// only the note: the rehearsal flag, the sheet and the evidence are never its.
func (a *checkoutAPI) checkoutSeal(w http.ResponseWriter, r *http.Request) {
	a.checkoutWriteAs(w, r, PermSignoff, "", func(pr auth.Principal, req checkoutRequest) (any, int, error) {
		if a.s.tagWriter != nil {
			return nil, http.StatusNotImplemented, fmt.Errorf("this server mirrors a controller it does not run: seal on that controller")
		}
		run, ok := a.co.Store.Get(r.PathValue("id"))
		if !ok {
			return nil, http.StatusNotFound, checkout.ErrNoRun
		}
		// a live force the server holds (one being started or ended) is not
		// something to sign over, whatever the store has recorded yet
		a.co.ck.mu.Lock()
		forcing := a.co.ck.force != nil
		a.co.ck.mu.Unlock()
		if forcing {
			return nil, http.StatusConflict, fmt.Errorf("%w: a checkout force is starting, standing or ending: let it go first", checkout.ErrSealRefused)
		}
		if a.co.Evidence == nil {
			return nil, http.StatusConflict, fmt.Errorf("%w: this controller cannot say its devices are real (no evidence of its drivers): nothing signs a checkout it cannot vouch for", checkout.ErrSealRefused)
		}
		ev, why := a.co.Evidence(a.co.Sheet)
		if why != "" {
			return nil, http.StatusConflict, fmt.Errorf("%w: %s", checkout.ErrSealRefused, why)
		}
		sr, err := a.co.Store.Seal(checkout.SealInput{Run: run.ID, Sheet: a.co.Sheet, By: noteAuthor(pr.String()), Principal: pr.String(),
			Note: req.Note, Mode: a.co.Signoff, Evidence: ev})
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		return sealedView{Run: run.ID, Fingerprint: a.co.Store.KeyFingerprint(), Record: sr}, http.StatusCreated, nil
	})
}

// sealedView is the seal's answer: the record, and the key's fingerprint.
type sealedView struct {
	Run         string                `json:"run"`
	Fingerprint string                `json:"fingerprint"`
	Record      checkout.SealedRecord `json:"record"`
}

// handleCheckoutSealed serves a sealed run's record as the file it is: the
// bytes the signature covers are in it. A read like the run's own.
func (a *checkoutAPI) handleCheckoutSealed(w http.ResponseWriter, r *http.Request) {
	if a.checkoutOff(w) {
		return
	}
	b, err := a.co.Store.ReadSealed(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that run is not sealed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+checkout.FileStem(r.PathValue("id"))+`-sealed.json"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}
