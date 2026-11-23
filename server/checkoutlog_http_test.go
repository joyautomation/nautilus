//go:build !js

package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/checkout"
)

// What a pair's log says to a write is what the person is told: no majority is
// refused (nothing waiting), an unconfirmed proposal is "outcome unknown"
// (it may be recorded), and a halted store says why.
func TestAPairsWriteAnswersSayWhetherAnythingIsWaiting(t *testing.T) {
	st, err := checkout.OpenLog(ckDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	var answer error
	st.AttachLog(func(context.Context, string, []byte) (uint64, error) { return 0, answer })
	co := &CheckoutOptions{Store: st, Sheet: ckSheet(), Signoff: "two-person"}
	srv, _, _ := patServerWith(t, ckOpts(Options{}, co))
	ckReg(srv, co)
	eng := ckAs(t, srv, "akhan")
	answer = checkout.ErrNoMajority
	rec := call(t, srv, "POST", "/api/checkout/runs", `{}`, eng)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get(outcomeHeader) != "refused" || !strings.Contains(rec.Body.String(), "majority") {
		t.Errorf("no majority: %d %q %s", rec.Code, rec.Header().Get(outcomeHeader), rec.Body)
	}
	answer = checkout.ErrUnknown
	rec = call(t, srv, "POST", "/api/checkout/runs", `{}`, eng)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get(outcomeHeader) != "unknown" {
		t.Errorf("not confirmed: %d %q %s", rec.Code, rec.Header().Get(outcomeHeader), rec.Body)
	}
	// a store that could not read an entry says so on every route
	st.ApplyLogged(1, 1, []byte(`{"v":99,"kind":"run"}`))
	rec = call(t, srv, "GET", "/api/checkout", "", eng)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "newer build") {
		t.Errorf("a halted store: %d %s", rec.Code, rec.Body)
	}
}
