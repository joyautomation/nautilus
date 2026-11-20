package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/joyautomation/nautilus/oplog"
)

// An entry over the operator-intent log's size cap was never proposed: the
// answer is a known refusal (413), not "outcome unknown" (#1050 review).
func TestTooLargeProposalIsAKnownRefusal(t *testing.T) {
	rec := httptest.NewRecorder()
	unknownOutcome(rec, fmt.Errorf("%w: 600000 bytes", oplog.ErrTooLarge))
	if rec.Code != http.StatusRequestEntityTooLarge || rec.Header().Get(outcomeHeader) != "refused" {
		t.Fatalf("a too-large entry answered %d (outcome %q), want 413, refused", rec.Code, rec.Header().Get(outcomeHeader))
	}
	rec = httptest.NewRecorder()
	unknownOutcome(rec, fmt.Errorf("%w: %w", oplog.ErrReadOnly, oplog.ErrUnconfirmed))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get(outcomeHeader) != "unknown" {
		t.Fatalf("an unconfirmed write answered %d (outcome %q)", rec.Code, rec.Header().Get(outcomeHeader))
	}
}
