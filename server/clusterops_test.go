package server

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/leader"
)

// The clear-duplicate route under an auth: block: an administrator's
// browser session needs a password entered within auth.elevation (the
// HMI asks for it), a personal access token counts as fresh, and the
// command is told who asked (#1029 3c-3a).
func TestClearDuplicateAsksForAFreshPassword(t *testing.T) {
	pair := &fakePair{outcome: "done"}
	srv := auditServer(t, Options{Cluster: pair})
	id, pat, at := elevateAs(t, srv)
	post := func(mod ...func(*http.Request)) int {
		return call(t, srv, "POST", "/api/cluster/clear-duplicate", `{"name":"a","copyStopped":true}`, append(mod, ownOrigin, fromLoopback)...).Code
	}
	at(15 * time.Minute)
	if code := post(withCookie(id)); code != http.StatusUnauthorized {
		t.Fatalf("a lapsed session: %d, want 401", code)
	}
	if len(pair.by) != 0 {
		t.Fatalf("the command ran for a lapsed session: %v", pair.by)
	}
	at(15*time.Minute - time.Second)
	if code := post(withCookie(id)); code != http.StatusOK {
		t.Fatalf("a fresh session: %d", code)
	}
	if code := post(bearer(pat)); code != http.StatusOK {
		t.Fatalf("a personal access token: %d", code)
	}
	if !slices.Equal(pair.by, []string{"user:boss", "user:boss"}) {
		t.Fatalf("the command was told %v, want the user each time", pair.by)
	}
}

// leaderlessPair is a pair's standby that knows no leader (an election
// under way, a pair not yet commissioned): the time a clear is needed.
type leaderlessPair struct{ fakePair }

func (f *leaderlessPair) Status() leader.Status {
	return leader.Status{Mode: "quorum", Pod: "b", Marked: []string{"a"}}
}

// A standby that knows no leader refuses writes with a 503 (#621), but
// answers the pair's operator commands itself: they act on its members,
// not on a leader's tag store, and are needed exactly when nobody leads.
func TestAStandbyWithNoLeaderAnswersClearDuplicate(t *testing.T) {
	pair := &leaderlessPair{fakePair{outcome: "done"}}
	srv := auditServer(t, Options{AuthToken: "s3cret", Cluster: pair})
	rec := call(t, srv, "POST", "/api/cluster/clear-duplicate", `{"name":"a","copyStopped":true}`, bearer("s3cret"))
	if rec.Code != http.StatusOK || len(pair.by) != 1 {
		t.Fatalf("clear on a leaderless standby: %d %s (ran %d times)", rec.Code, rec.Body, len(pair.by))
	}
	// A new pair has no leader until it is commissioned, and a controller
	// that lost its state knows none until it is re-commissioned (#1029
	// 3c-3c): both commands are answered here.
	for i, c := range []struct{ path, body string }{
		{"/api/cluster/commission", `{}`},
		{"/api/cluster/recommission", `{"floor":5000}`},
		{"/api/cluster/recommission", `{"fromSpare":true,"copyStopped":true}`},
	} {
		if rec := call(t, srv, "POST", c.path, c.body, bearer("s3cret")); rec.Code != http.StatusOK || len(pair.by) != 2+i {
			t.Fatalf("%s %s on a leaderless standby: %d %s", c.path, c.body, rec.Code, rec.Body)
		}
	}
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`, bearer("s3cret")); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a tag write on the same standby: %d, want the 503 (the test's premise)", rec.Code)
	}
}

// A server outside a pair answers the commissioning routes 404, and
// journals the refusal: the audit seam reads no cluster that isn't there.
func TestTheCommissioningRoutesOutsideAPairAnswer404(t *testing.T) {
	srv := auditServer(t, Options{AuthToken: "s3cret"})
	for _, c := range []struct{ path, body string }{
		{"/api/cluster/commission", `{}`},
		{"/api/cluster/recommission", `{"floor":5000}`},
		{"/api/cluster/recommission", `{"fromSpare":true,"copyStopped":true}`},
	} {
		if rec := call(t, srv, "POST", c.path, c.body, bearer("s3cret")); rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s outside a pair: %d %s", c.path, c.body, rec.Code, rec.Body)
		}
	}
}
