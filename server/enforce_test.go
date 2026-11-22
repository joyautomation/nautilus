package server

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
)

// The server's permission names are the auth package's (naut-lead's
// condition on #708 PR 4): a rename on one side must not leave a route
// asking for a permission no role has.
func TestPermsMatchAuth(t *testing.T) {
	for server, a := range map[string]string{
		PermWrite: auth.Write, PermAck: auth.Ack, PermShelve: auth.Shelve, PermSubstitute: auth.Substitute,
		PermForce: auth.Force, PermSFC: auth.SFC, PermEdit: auth.Edit, PermActivate: auth.Activate,
		PermDeploy: auth.Deploy, PermConfig: auth.Config, PermAudit: auth.Audit,
	} {
		if server != a || !auth.IsPermission(server) {
			t.Errorf("server permission %q is not auth's %q", server, a)
		}
	}
}

// Every action the seam journals needs a permission (actionPerm): with an
// auth: block, one without is refused to everyone.
func TestEveryActionHasAPermission(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	n := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "audited" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true // TestEveryActionIsClassed refuses it
			}
			action, _ := strconv.Unquote(lit.Value)
			n++
			if perm := actionPerm[action]; !auth.IsPermission(perm) {
				t.Errorf("%s: action %q has no permission in actionPerm", fset.Position(call.Pos()), action)
			}
			return true
		})
	}
	if n < 12 {
		t.Fatalf("found %d audited actions", n)
	}
}

// enforce puts an auth: block on a row's server: NAUTILUS_TOKEN
// (admin-secret) is the administrator, "viewer" (v-secret) only reads,
// and "fenced" (f-secret) is an administrator over an area no row's
// target is in.
func enforce(t *testing.T, srv *Server) {
	t.Helper()
	p, err := auth.Compile(auth.Settings{
		Areas: map[string][]string{"nowhere": {"ZZZ_*"}},
		ServiceTokens: map[string][]auth.Grant{
			"viewer": {{Role: auth.Viewer}},
			"fenced": {{Role: auth.Administrator, Areas: []string{"nowhere"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.policy, srv.authToken = p, "admin-secret"
	srv.services = map[string]string{"viewer": "v-secret", "fenced": "f-secret"}
}

// enforceExactly puts an auth: block on srv with, for every permission,
// a token holding only it (on top of read) and one holding every
// permission but it: secrets "only-<perm>" and "but-<perm>".
func enforceExactly(t *testing.T, srv *Server) {
	t.Helper()
	set := auth.Settings{Roles: map[string]auth.RoleDef{}, ServiceTokens: map[string][]auth.Grant{}}
	srv.services = map[string]string{}
	for _, perm := range auth.Permissions {
		set.Roles["only-"+perm] = auth.RoleDef{Inherits: auth.Viewer, Allow: []string{perm}}
		set.Roles["but-"+perm] = auth.RoleDef{Inherits: auth.Administrator, Deny: []string{perm}}
		set.Roles["pair-"+perm] = auth.RoleDef{Inherits: auth.Viewer, Allow: []string{perm, auth.Write}}
		set.ServiceTokens["pair_"+perm] = []auth.Grant{{Role: "pair-" + perm}}
		srv.services["pair_"+perm] = "pair-" + perm
		set.ServiceTokens["only_"+perm] = []auth.Grant{{Role: "only-" + perm}}
		set.ServiceTokens["but_"+perm] = []auth.Grant{{Role: "but-" + perm}}
		srv.services["only_"+perm], srv.services["but_"+perm] = "only-"+perm, "but-"+perm
	}
	p, err := auth.Compile(set)
	if err != nil {
		t.Fatal(err)
	}
	srv.policy = p
}

// Each write route needs its own action's permission and nothing more:
// a token holding only it is accepted, and one holding every other
// permission is refused. This pins actionPerm, and the seam asking the
// policy for that permission rather than for anything else.
func TestEachActionNeedsExactlyItsPermission(t *testing.T) {
	for _, row := range auditRows() {
		if row.result != "ok" || row.person {
			continue
		}
		perm := actionPerm[row.action]
		cases := []struct {
			tok string
			ok  bool
		}{{"only-" + perm, true}, {"but-" + perm, false}}
		if row.also != "" {
			// Needs its permission AND the second: the permission alone is
			// refused, the pair accepted.
			cases = []struct {
				tok string
				ok  bool
			}{{"pair-" + perm, true}, {"only-" + perm, false}, {"but-" + perm, false}}
		}
		for _, c := range cases {
			srv := row.server(t)
			if row.prep != nil {
				row.prep(t, srv)
			}
			enforceExactly(t, srv)
			rec := call(t, srv, row.method, row.path, row.body, func(r *http.Request) {
				for k, v := range row.header {
					r.Header.Set(k, v)
				}
				r.Header.Del("X-Nautilus-Token")
				r.Header.Set("Authorization", "Bearer "+c.tok)
			})
			if ok := rec.Code < 300; ok != c.ok {
				t.Errorf("%s with %s: %d %s", row.name, c.tok, rec.Code, rec.Body)
			}
			// A refusal by role ran nothing, and says so: a client reading
			// the outcome (the kit's substitutes, IO-Link parameters) must
			// not take it as "may still take effect".
			if !c.ok && rec.Header().Get(outcomeHeader) != "refused" {
				t.Errorf("%s with %s: %d, %s %q, want refused", row.name, c.tok, rec.Code, outcomeHeader, rec.Header().Get(outcomeHeader))
			}
		}
	}
}

// writeState is what a write could change: every tag's value and every force.
func writeState(srv *Server) string {
	return fmt.Sprint(srv.rt.Tags().All(), srv.rt.Tags().ForcedValues())
}

// runAs drives an accepted row under an auth: block with token, and
// returns the status and the one entry it left.
func runAs(t *testing.T, row auditRow, tok string) (int, string, string, map[string]any, bool) {
	t.Helper()
	srv := row.server(t)
	if row.prep != nil {
		row.prep(t, srv)
	}
	enforce(t, srv)
	before, n := writeState(srv), len(srv.audit.ring.Recent())
	rec := call(t, srv, row.method, row.path, row.body, func(r *http.Request) {
		for k, v := range row.header {
			r.Header.Set(k, v)
		}
		r.Header.Del("X-Nautilus-Token")
		r.Header.Set("Authorization", "Bearer "+tok)
	})
	es := srv.audit.ring.Recent()
	if len(es)-n != 1 {
		t.Fatalf("%s: %d entries", row.name, len(es)-n)
	}
	return rec.Code, es[0].User, es[0].Result, es[0].Detail, writeState(srv) == before
}

// With an auth: block, every write route refuses a principal without its
// permission, journals the refusal as that principal, and changes
// nothing; NAUTILUS_TOKEN (the administrator) is refused nothing a row was
// accepted for.
func TestAuthBlockDecidesEveryWrite(t *testing.T) {
	n := 0
	for _, row := range auditRows() {
		if row.result != "ok" || row.person {
			continue
		}
		n++
		code, user, result, detail, unchanged := runAs(t, row, "v-secret")
		if code != http.StatusForbidden || user != "token:viewer" || result != "refused" || detail["status"] != http.StatusForbidden || !unchanged {
			t.Errorf("%s as a viewer: %d %s %s %v, unchanged %v", row.name, code, user, result, detail, unchanged)
		}
		code, user, result, _, _ = runAs(t, row, "admin-secret")
		if code >= 300 || user != "token" || result != "ok" {
			t.Errorf("%s as NAUTILUS_TOKEN: %d %s %s", row.name, code, user, result)
		}
	}
	if n < 15 {
		t.Fatalf("only %d accepted rows", n)
	}
}

// A known principal refused by role is journalled with what it tried to
// change (the body is read for the target), so the journal says which tag
// a named person was refused; a request with no credential is journalled
// without its body, as before.
func TestRoleRefusalJournalsItsTarget(t *testing.T) {
	n := 0
	for _, row := range auditRows() {
		if row.result != "ok" || row.target == "" || row.person {
			continue
		}
		want := ""
		if refusalTargets[row.action] != nil {
			want = row.target
			n++
		}
		for _, c := range []struct {
			auth   string
			target string
		}{{"Bearer v-secret", want}, {"", ""}, {"Bearer garbage", ""}} {
			srv := row.server(t)
			if row.prep != nil {
				row.prep(t, srv)
			}
			enforce(t, srv)
			k := len(srv.audit.ring.Recent())
			rec := call(t, srv, row.method, row.path, row.body, func(r *http.Request) {
				for k, v := range row.header {
					r.Header.Set(k, v)
				}
				r.Header.Del("X-Nautilus-Token")
				if c.auth != "" {
					r.Header.Set("Authorization", c.auth)
				}
			})
			es := srv.audit.ring.Recent()
			if len(es)-k != 1 || rec.Code < 400 {
				t.Fatalf("%s (%q): %d, %d entries", row.name, c.auth, rec.Code, len(es)-k)
			}
			if e := es[0]; e.Result != "refused" || e.Target != c.target || e.Before != nil {
				t.Errorf("%s (%q): %s target %q before %v, want refused target %q, no before", row.name, c.auth, e.Result, e.Target, e.Before, c.target)
			}
		}
	}
	if n < 8 {
		t.Fatalf("only %d accepted rows with a refusal target", n)
	}
}

// Every action a refusal names a target for is a journalled action, so a
// misspelt key cannot silently drop the target.
func TestRefusalTargetsAreActions(t *testing.T) {
	for a := range refusalTargets {
		if _, ok := actionPerm[a]; !ok {
			t.Errorf("refusalTargets names %q, which no route journals", a)
		}
	}
}

// A refusal reads at most refusedBodyMax of a known principal's body, and
// names nothing past it; an anonymous request's body is not read at all.
func TestRefusalBodyIsBounded(t *testing.T) {
	big := `{"name":"SP","value":"` + strings.Repeat("x", refusedBodyMax) + `"}`
	for _, c := range []struct {
		what, auth, body, target string
		maxRead                  int
	}{
		{"viewer, small body", "Bearer v-secret", `{"name":"SP","value":1}`, "SP", refusedBodyMax + 1},
		{"viewer, body past the cap", "Bearer v-secret", big, "", refusedBodyMax + 1},
		// A small value padded past the cap: its first JSON value would
		// parse from the part read, but a body past the cap names nothing.
		{"viewer, padded past the cap", "Bearer v-secret", `{"name":"SP","value":1}` + strings.Repeat(" ", refusedBodyMax), "", refusedBodyMax + 1},
		{"anonymous", "", `{"name":"SP","value":1}`, "", 0},
	} {
		srv := auditServer(t, Options{})
		enforce(t, srv)
		body := &countingBody{r: strings.NewReader(c.body)}
		n := len(srv.audit.ring.Recent())
		rec := call(t, srv, "POST", "/api/forces", "", func(r *http.Request) {
			r.Body = body
			if c.auth != "" {
				r.Header.Set("Authorization", c.auth)
			}
		})
		es := srv.audit.ring.Recent()
		if rec.Code < 400 || len(es)-n != 1 {
			t.Fatalf("%s: %d, %d entries", c.what, rec.Code, len(es)-n)
		}
		if es[0].Target != c.target || body.n > c.maxRead {
			t.Errorf("%s: target %q (want %q), %d bytes read (at most %d)", c.what, es[0].Target, c.target, body.n, c.maxRead)
		}
	}
}

// Every action whose permission areas scope (write, ack, shelve,
// substitute, force) checks the target it acts on: a principal holding the
// permission only over another area is refused, and nothing changes. An
// area-scoped action with no accepted row fails here, so a new route
// cannot skip the check.
func TestEveryAreaScopedActionChecksItsTarget(t *testing.T) {
	covered := map[string]bool{}
	for _, row := range auditRows() {
		// An ack link, an SMS reply and a keypress act as the person they
		// name, never the request's token: their area checks are
		// TestAckLinkActsOnlyAsAPersonWhoMayAck and
		// TestInboundActsOnlyAsAPersonWhoMayAck.
		if row.result != "ok" || !auth.AreaScoped(actionPerm[row.action]) || row.pattern == "POST /alarm-ack/{token}" ||
			strings.HasPrefix(row.pattern, "POST "+InboundPath) {
			continue
		}
		covered[row.action] = true
		code, user, result, _, unchanged := runAs(t, row, "f-secret")
		if code != http.StatusForbidden || user != "token:fenced" || result != "refused" || !unchanged {
			t.Errorf("%s by a principal fenced to another area: %d %s %s, unchanged %v", row.name, code, user, result, unchanged)
		}
	}
	for action, perm := range actionPerm {
		if auth.AreaScoped(perm) && !covered[action] {
			t.Errorf("%s (%s) has no accepted row to check its target with", action, perm)
		}
	}
}

// A service token is honoured wherever NAUTILUS_TOKEN is: by a Host no
// page can be trusted on, and for a page on another origin.
func TestServiceTokenPassesTheHostRuleAndCORS(t *testing.T) {
	srv := New(newTestRuntime(t))
	enforce(t, srv)
	get := func(tok string) *http.Request {
		r, _ := http.NewRequest("GET", "http://plc.example/api/state", nil)
		r.Host = "plc.example"
		r.Header.Set("Origin", "http://hmi.example")
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		return r
	}
	for tok, want := range map[string]int{"v-secret": http.StatusOK, "guess": http.StatusForbidden, "": http.StatusForbidden} {
		rec := serve(srv, get(tok))
		if rec.Code != want {
			t.Errorf("token %q by an untrusted Host: %d, want %d", tok, rec.Code, want)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); (tok == "v-secret") != (got == "http://hmi.example") {
			t.Errorf("token %q: Access-Control-Allow-Origin %q", tok, got)
		}
	}
}

// GET /api/me says who a request is and what it may do.
func TestMe(t *testing.T) {
	srv := New(newTestRuntime(t))
	if body := call(t, srv, "GET", "/api/me", "").Body.String(); !strings.Contains(body, `"auth":false`) || !strings.Contains(body, `"principal":"anonymous"`) {
		t.Errorf("without auth: %s", body)
	}
	enforce(t, srv)
	for tok, want := range map[string]string{
		// The areas a caller's permissions name carry their tag patterns
		// (the HMI kit greys out a control by them, #708 PR 7b); a caller
		// holding none gets no areas field.
		"f-secret": `"principal":"token:fenced","roles":["administrator"],"permissions":{"*":["read","sfc","edit","activate","deploy","config","audit","users","override","redundancy"],"nowhere":["write","ack","shelve","note","substitute","force"]},"areas":{"nowhere":["zzz_*"]}`,
		"v-secret": `"principal":"token:viewer","roles":["viewer"],"permissions":{"*":["read"]},"elevated"`,
		"guess":    `"principal":"anonymous","roles":[],"permissions":{}`,
	} {
		body := call(t, srv, "GET", "/api/me", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }).Body.String()
		if !strings.Contains(body, want) || !strings.Contains(body, `"auth":true`) {
			t.Errorf("%s: %s\nwant %s", tok, body, want)
		}
	}
}

func serve(srv *Server, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

// Outside the audit seam (a facade's own endpoint), AuthorizeWrite under
// an auth: block needs write over every tag.
func TestAuthorizeWriteOutsideTheSeam(t *testing.T) {
	srv := New(newTestRuntime(t))
	enforce(t, srv)
	for tok, want := range map[string]int{"admin-secret": 0, "v-secret": http.StatusForbidden, "f-secret": http.StatusForbidden, "": http.StatusUnauthorized, "guess": http.StatusUnauthorized} {
		r := httptest.NewRequest("POST", "http://localhost/x", nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		if code, msg := srv.AuthorizeWrite(r); code != want {
			t.Errorf("token %q: %d %s, want %d", tok, code, msg, want)
		}
	}
}

// With an auth: block, an action the server has not mapped to a
// permission is refused to everyone, the administrator included.
func TestUnmappedActionIsRefused(t *testing.T) {
	srv := New(newTestRuntime(t))
	enforce(t, srv)
	r := httptest.NewRequest("POST", "http://localhost/x", nil)
	r.Header.Set("Authorization", "Bearer admin-secret")
	if code, _, _ := srv.preAuthorize(r, "made.up"); code != http.StatusForbidden {
		t.Errorf("an unmapped action: %d", code)
	}
	if code, _, _ := srv.preAuthorize(r, "tag.write"); code != 0 {
		t.Errorf("a mapped one: %d", code)
	}
}

// An id that names no active substitute is a 404 for anyone holding
// substitute, the administrator and an area-fenced principal alike: there
// is no tag to authorize over, so it is answered before the target is
// (#1035). A malformed id is a 400 the same way; a viewer, who holds no
// substitute at all, is still refused by the seam.
func TestSubstituteStopOfNoneIsNotFound(t *testing.T) {
	srv := subAuditServer(t)
	enforce(t, srv)
	for _, c := range []struct {
		path, tok string
		want      int
		reason    string
	}{
		{"/api/substitutes/999", "admin-secret", http.StatusNotFound, "no substitute 999 is active"},
		{"/api/substitutes/999", "f-secret", http.StatusNotFound, "no substitute 999 is active"},
		{"/api/substitutes/x", "admin-secret", http.StatusBadRequest, "is a number"},
		{"/api/substitutes/999", "v-secret", http.StatusForbidden, ""},
	} {
		rec := call(t, srv, "DELETE", c.path, "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+c.tok) })
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.reason) {
			t.Errorf("DELETE %s with %s: %d %s, want %d %q", c.path, c.tok, rec.Code, rec.Body, c.want, c.reason)
		}
		es := srv.audit.ring.Recent()
		if len(es) == 0 || es[0].Result != "refused" || es[0].Detail["status"] != c.want {
			t.Errorf("DELETE %s with %s: journal %+v", c.path, c.tok, es)
		}
	}
}

// Clearing every force clears only the forces it checked: one set in
// another area between the check and the clear is left for its own
// clear, not removed unchecked (#1035).
func TestClearAllForcesClearsOnlyWhatItChecked(t *testing.T) {
	srv := auditServer(t, Options{})
	p, err := auth.Compile(auth.Settings{
		Areas:         map[string][]string{"a": {"SP"}, "b": {"Out"}},
		ServiceTokens: map[string][]auth.Grant{"eng_a": {{Role: auth.Engineer, Areas: []string{"a"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.policy, srv.services = p, map[string]string{"eng_a": "a-secret"}
	if err := srv.rt.Tags().Force("SP", 5.0); err != nil {
		t.Fatal(err)
	}
	srv.unforceAllChecked = func() {
		if err := srv.rt.Tags().Force("Out", 1.0); err != nil {
			t.Fatal(err)
		}
	}
	rec := call(t, srv, "POST", "/api/forces/clear", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer a-secret") })
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"removed":1`) {
		t.Errorf("clear all by area a: %d %s", rec.Code, rec.Body)
	}
	if got := fmt.Sprint(srv.rt.Tags().ForcedValues()); got != "map[Out:1]" {
		t.Errorf("forced after the clear: %s, want only area b's Out", got)
	}
}

// A known principal's refused request whose body stalls is answered once
// refusedBodyWait passes (over a real connection, where the read deadline
// applies), journalled with no target: a low-role account cannot hold the
// seam open by trickling a body.
func TestRefusalBodyReadHasADeadline(t *testing.T) {
	defer func(w time.Duration) { refusedBodyWait = w }(refusedBodyWait)
	refusedBodyWait = 200 * time.Millisecond
	srv := auditServer(t, Options{})
	enforce(t, srv)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	pr, pw := io.Pipe()
	defer pw.Close()
	// A whole value, then nothing: what was read must not be parsed.
	go pw.Write([]byte(`{"name":"SP","value":1}`))
	req, err := http.NewRequest("POST", ts.URL+"/api/forces", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer v-secret")
	n := len(srv.audit.ring.Recent())
	start := time.Now()
	res, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	res.Body.Close()
	if took := time.Since(start); res.StatusCode != http.StatusForbidden || took > 5*time.Second {
		t.Fatalf("%d after %v, want 403 within the deadline", res.StatusCode, took)
	}
	es := srv.audit.ring.Recent()
	if len(es)-n != 1 || es[0].Result != "refused" || es[0].Target != "" || es[0].Detail["body"] != "unread" {
		t.Errorf("entries %+v, want one refusal with no target and its body unread", es[:len(es)-n])
	}
}

// A person's refusal is not folded into a summary by anyone else's: five
// refusals with no credential from the same address fill that window,
// and a viewer's probe after them is still journalled in full, with its
// target.
func TestNamedRefusalHasItsOwnWindow(t *testing.T) {
	srv := auditServer(t, Options{})
	enforce(t, srv)
	for i := 0; i < refuseFull+2; i++ {
		call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`)
	}
	n := len(srv.audit.ring.Recent())
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":2}`, func(r *http.Request) { r.Header.Set("Authorization", "Bearer v-secret") })
	es := srv.audit.ring.Recent()
	if len(es)-n != 1 || es[0].User != "token:viewer" || es[0].Target != "SP" {
		t.Errorf("the viewer's refusal after an anonymous flood: %+v", es[:len(es)-n])
	}
}

// Past cap(refusedReads) refused bodies at once, a refusal reads nothing
// and names only what its path does, saying the body went unread; a body
// past the cap does the same.
func TestRefusedBodyUnreadKeepsThePath(t *testing.T) {
	for _, c := range []struct {
		what, method, path, body, target string
		busy                             bool
	}{
		{"too many reads at once", "POST", "/api/forces", `{"name":"SP","value":1}`, "", true},
		{"too many reads, a path target", "DELETE", "/api/forces/SP", `{}`, "SP", true},
		{"a body past the cap, a path target", "DELETE", "/api/forces/SP", strings.Repeat(" ", refusedBodyMax+1), "SP", false},
	} {
		srv := auditServer(t, Options{})
		enforce(t, srv)
		if c.busy {
			for i := 0; i < cap(refusedReads); i++ {
				refusedReads <- struct{}{}
			}
		}
		body := &countingBody{r: strings.NewReader(c.body)}
		n := len(srv.audit.ring.Recent())
		call(t, srv, c.method, c.path, "", func(r *http.Request) {
			r.Body = body
			r.Header.Set("Authorization", "Bearer v-secret")
		})
		if c.busy {
			for i := 0; i < cap(refusedReads); i++ {
				<-refusedReads
			}
		}
		es := srv.audit.ring.Recent()
		if len(es)-n != 1 {
			t.Fatalf("%s: %d entries", c.what, len(es)-n)
		}
		e := es[0]
		if e.Result != "refused" || e.Target != c.target || e.Detail["body"] != "unread" || (c.busy && body.n != 0) {
			t.Errorf("%s: %s target %q detail %v, %d bytes read", c.what, e.Result, e.Target, e.Detail, body.n)
		}
	}
}

// Where the seam cannot set a read deadline (a writer that does not let
// it, like a test recorder or an embedder's middleware), a refused body
// that never comes still does not hold the request past refusedBodyWait.
func TestRefusedBodyWaitWithoutADeadline(t *testing.T) {
	defer func(w time.Duration) { refusedBodyWait = w }(refusedBodyWait)
	refusedBodyWait = 100 * time.Millisecond
	srv := auditServer(t, Options{})
	enforce(t, srv)
	pr, pw := io.Pipe()
	defer pw.Close() // ends the read, freeing its slot
	n := len(srv.audit.ring.Recent())
	done := make(chan struct{})
	go func() {
		call(t, srv, "POST", "/api/forces", "", func(r *http.Request) {
			r.Body = pr
			r.Header.Set("Authorization", "Bearer v-secret")
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a refused request with a stalled body was held past refusedBodyWait")
	}
	es := srv.audit.ring.Recent()
	if len(es)-n != 1 || es[0].Detail["body"] != "unread" {
		t.Errorf("entries %+v, want one refusal whose body went unread", es[:len(es)-n])
	}
}

// failingReader fails every read: a connection that breaks mid-body.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// A refused body whose read fails after a whole value is not parsed: what
// arrived before the error names nothing, and the entry says the body went
// unread.
func TestRefusedBodyReadErrorNamesNothing(t *testing.T) {
	srv := auditServer(t, Options{})
	enforce(t, srv)
	n := len(srv.audit.ring.Recent())
	call(t, srv, "POST", "/api/forces", "", func(r *http.Request) {
		r.Body = io.NopCloser(io.MultiReader(strings.NewReader(`{"name":"SP","value":1}`), failingReader{}))
		r.Header.Set("Authorization", "Bearer v-secret")
	})
	es := srv.audit.ring.Recent()
	if len(es)-n != 1 || es[0].Target != "" || es[0].Detail["body"] != "unread" {
		t.Errorf("entries %+v, want one refusal with no target and its body unread", es[:len(es)-n])
	}
}
