package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
		if row.result != "ok" {
			continue
		}
		perm := actionPerm[row.action]
		for _, c := range []struct {
			tok string
			ok  bool
		}{{"only-" + perm, true}, {"but-" + perm, false}} {
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
		}
	}
}

// state is what a write could change: every tag's value and every force.
func state(srv *Server) string {
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
	before, n := state(srv), len(srv.audit.ring.Recent())
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
	return rec.Code, es[0].User, es[0].Result, es[0].Detail, state(srv) == before
}

// With an auth: block, every write route refuses a principal without its
// permission, journals the refusal as that principal, and changes
// nothing; NAUTILUS_TOKEN (the administrator) is refused nothing a row was
// accepted for.
func TestAuthBlockDecidesEveryWrite(t *testing.T) {
	n := 0
	for _, row := range auditRows() {
		if row.result != "ok" {
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

// Every action whose permission areas scope (write, ack, shelve,
// substitute, force) checks the target it acts on: a principal holding the
// permission only over another area is refused, and nothing changes. An
// area-scoped action with no accepted row fails here, so a new route
// cannot skip the check.
func TestEveryAreaScopedActionChecksItsTarget(t *testing.T) {
	covered := map[string]bool{}
	for _, row := range auditRows() {
		if row.result != "ok" || !auth.AreaScoped(actionPerm[row.action]) {
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
		"f-secret": `"principal":"token:fenced","roles":["administrator"],"permissions":{"*":["read","sfc","edit","activate","deploy","config","audit","users","override"],"nowhere":["write","ack","shelve","substitute","force"]}`,
		"v-secret": `"principal":"token:viewer","roles":["viewer"],"permissions":{"*":["read"]}`,
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
	if code, _ := srv.preAuthorize(r, "made.up"); code != http.StatusForbidden {
		t.Errorf("an unmapped action: %d", code)
	}
	if code, _ := srv.preAuthorize(r, "tag.write"); code != 0 {
		t.Errorf("a mapped one: %d", code)
	}
}
