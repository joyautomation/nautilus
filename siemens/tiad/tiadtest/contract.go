package tiadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// Target is one agent under the contract suite: the Go fake, tiad-fake
// (the C# agent's core with a scripted host, under Mono), or the real
// agent on a TIA machine. Each holds a second copy of the agent's rules,
// so the suite checks them over the wire, not through the client, which
// would refuse a bad request before it is sent.
type Target interface {
	// Client talks to the agent with the token it was started with.
	Client() *tiad.Client
	// SetGates scripts the machine gates probe reports (tia,
	// openness-group, openness-allowlist, licence). Nil means all pass.
	SetGates(t testing.TB, gates []tiad.Gate)
}

// Config is the agent-side configuration a Target starts with: what the
// real agent reads from TIAD_TOKEN and TIAD_TARGETS. An empty Token means
// a random one: the agent never runs without a token.
type Config struct {
	Token   string
	Targets []string
}

// Starter starts a fresh agent for one test.
type Starter func(t *testing.T, cfg Config) Target

// GoFake starts the in-memory Go fake.
func GoFake(t *testing.T, cfg Config) Target {
	f := New()
	if cfg.Token != "" {
		f.Token = cfg.Token
	}
	f.Targets = cfg.Targets
	_, c := f.Serve(t)
	return goFake{f, c}
}

type goFake struct {
	f *Fake
	c *tiad.Client
}

func (g goFake) Client() *tiad.Client                     { return g.c }
func (g goFake) SetGates(_ testing.TB, gates []tiad.Gate) { g.f.SetGates(gates) }

// Contract runs the agent's HTTP contract (docs/design/siemens-ide.md §3)
// against the agents start makes.
func Contract(t *testing.T, start Starter) {
	t.Run("Health", func(t *testing.T) { contractHealth(t, start) })
	t.Run("Probe", func(t *testing.T) { contractProbe(t, start) })
	t.Run("Token", func(t *testing.T) { contractToken(t, start) })
	t.Run("NoBrowser", func(t *testing.T) { contractNoBrowser(t, start) })
	t.Run("ContentType", func(t *testing.T) { contractContentType(t, start) })
	t.Run("Files", func(t *testing.T) { contractFiles(t, start) })
	t.Run("PathsRefused", func(t *testing.T) { contractPathsRefused(t, start) })
	t.Run("PathsAccepted", func(t *testing.T) { contractPathsAccepted(t, start) })
}

var ctx = context.Background()

func contractHealth(t *testing.T, start Starter) {
	targets := []string{"192.0.2.10", "192.0.2.11"}
	c := start(t, Config{Targets: targets}).Client()
	h, err := c.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version == "" || h.Sessions != 0 || !reflect.DeepEqual(h.Targets, targets) {
		t.Fatalf("health = %+v, want a version, no sessions and targets %v", h, targets)
	}
}

func gateNames(gs []tiad.Gate) string {
	var n []string
	for _, g := range gs {
		n = append(n, g.Name)
	}
	return strings.Join(n, ",")
}

func contractProbe(t *testing.T, start Starter) {
	a := start(t, Config{})
	c := a.Client()
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const all = "tia,openness-group,openness-allowlist,licence,workdir"
	if !p.Usable || gateNames(p.Gates) != all {
		t.Fatalf("probe = %+v, want usable with gates %s", p, all)
	}
	for _, g := range p.Gates {
		if !g.OK {
			t.Errorf("gate %s failed: %s", g.Name, g.Detail)
		}
	}

	// A failed gate is a successful probe that says "not usable".
	a.SetGates(t, []tiad.Gate{{Name: "licence", OK: false, Detail: "trial expired"}})
	p, err = c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe with a failed gate: %v", err)
	}
	failed := false
	for _, g := range p.Gates {
		failed = failed || g.Name == "licence" && !g.OK && g.Detail == "trial expired"
	}
	if p.Usable || !failed {
		t.Fatalf("probe with a failed licence gate = %+v", p)
	}
	// Health and the files never need TIA, so no gate stops them.
	if _, err := c.Health(ctx); err != nil {
		t.Fatalf("health with a failed gate: %v", err)
	}
	if _, err := c.PutFile(ctx, "a.txt", []byte("x")); err != nil {
		t.Fatalf("put with a failed gate: %v", err)
	}
	if _, err := c.ListFiles(ctx); err != nil {
		t.Fatalf("list with a failed gate: %v", err)
	}
}

// raw sends a request without the client's own checks, with the client's
// token and, on a PUT, the file content type.
func raw(t *testing.T, c *tiad.Client, method, rawPath string, body []byte) (int, []byte) {
	t.Helper()
	h := http.Header{"Authorization": {"Bearer " + c.Token}}
	if method == http.MethodPut {
		h.Set("Content-Type", "application/octet-stream")
	}
	return rawWith(t, c, method, rawPath, h, body)
}

// rawWith sends a request with exactly the headers h. It never follows a
// redirect: an agent that redirects has not served the request.
func rawWith(t *testing.T, c *tiad.Client, method, rawPath string, h http.Header, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, c.BaseURL+rawPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header[k] = v
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// envelopeKind returns the error kind of an envelope, or "" if b is not
// a failed envelope.
func envelopeKind(b []byte) string {
	var env struct {
		OK    bool `json:"ok"`
		Error *struct {
			Kind string `json:"kind"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &env) != nil || env.OK || env.Error == nil {
		return ""
	}
	return env.Error.Kind
}

func files(t *testing.T, c *tiad.Client) []tiad.FileInfo {
	t.Helper()
	fs, err := c.ListFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func contractToken(t *testing.T, start Starter) {
	const token = "s3cret"
	c := start(t, Config{Token: token}).Client()
	if _, err := c.Health(ctx); err != nil {
		t.Fatalf("the right token: %v", err)
	}

	none := tiad.New(c.BaseURL, "")
	none.Token = ""
	if _, err := none.Health(ctx); tiad.Kind(err) != tiad.KindUnauthorized || !strings.Contains(err.Error(), "wants a token") {
		t.Fatalf("no token: %v", err)
	}
	wrong := tiad.New(c.BaseURL, "wrong")
	if _, err := wrong.Probe(ctx); tiad.Kind(err) != tiad.KindUnauthorized {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := wrong.PutFile(ctx, "a.txt", []byte("x")); tiad.Kind(err) != tiad.KindUnauthorized {
		t.Fatalf("put with a wrong token: %v", err)
	}
	if fs := files(t, c); len(fs) != 0 {
		t.Fatalf("a refused put wrote %v", fs)
	}

	// Only exactly "Bearer <token>" passes, on every call.
	for _, auth := range []string{"", "Bearer", "Bearer ", "Bearer s3cre", "Bearer s3cretX", "bearer s3cret", "Basic s3cret", "Bearer  s3cret", "s3cret"} {
		for _, call := range []struct{ method, path string }{{"GET", "/v1/health"}, {"GET", "/v1/files"}, {"PUT", "/v1/files/a.txt"}, {"GET", "/v1/nope"}} {
			h := http.Header{"Content-Type": {"application/octet-stream"}}
			if auth != "" {
				h.Set("Authorization", auth)
			}
			status, b := rawWith(t, c, call.method, call.path, h, []byte("x"))
			if status != http.StatusUnauthorized || envelopeKind(b) != tiad.KindUnauthorized {
				t.Errorf("%s %s with Authorization %q = HTTP %d %s, want 401 unauthorized", call.method, call.path, auth, status, b)
			}
		}
	}
	if fs := files(t, c); len(fs) != 0 {
		t.Fatalf("a refused put wrote %v", fs)
	}
}

// contractNoBrowser: a request with an Origin header is a browser's, and
// no browser ever calls the agent, whatever token it carries.
func contractNoBrowser(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	for _, call := range []struct{ method, path string }{{"GET", "/v1/health"}, {"GET", "/v1/files"}, {"PUT", "/v1/files/a.txt"}} {
		for _, origin := range []string{"http://127.0.0.1:8190", "null", "https://example.com"} {
			h := http.Header{"Authorization": {"Bearer " + c.Token}, "Origin": {origin}, "Content-Type": {"application/octet-stream"}}
			status, b := rawWith(t, c, call.method, call.path, h, []byte("x"))
			if status != http.StatusForbidden || envelopeKind(b) != tiad.KindUnauthorized {
				t.Errorf("%s %s with Origin %q = HTTP %d %s, want 403 unauthorized", call.method, call.path, origin, status, b)
			}
		}
	}
	if fs := files(t, c); len(fs) != 0 {
		t.Fatalf("a browser's put wrote %v", fs)
	}
}

// contractContentType: a file is put as application/octet-stream and
// nothing else; a browser can send text/plain without asking first.
func contractContentType(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	for _, ct := range []string{"", "text/plain", "application/json", "multipart/form-data; boundary=x", "application/x-www-form-urlencoded"} {
		h := http.Header{"Authorization": {"Bearer " + c.Token}}
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		status, b := rawWith(t, c, "PUT", "/v1/files/a.txt", h, []byte("x"))
		if status != http.StatusUnsupportedMediaType || envelopeKind(b) != tiad.KindBadRequest {
			t.Errorf("PUT with Content-Type %q = HTTP %d %s, want 415 bad_request", ct, status, b)
		}
	}
	if fs := files(t, c); len(fs) != 0 {
		t.Fatalf("a refused put wrote %v", fs)
	}
	h := http.Header{"Authorization": {"Bearer " + c.Token}, "Content-Type": {"Application/Octet-Stream; x=y"}}
	if status, b := rawWith(t, c, "PUT", "/v1/files/a.txt", h, []byte("x")); status != http.StatusOK {
		t.Fatalf("PUT with a parameterised octet-stream = HTTP %d %s", status, b)
	}
}

func contractFiles(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	fi, err := c.PutFile(ctx, "dir/sub/all.bin", all)
	if err != nil || fi.Path != "dir/sub/all.bin" || fi.Size != 256 {
		t.Fatalf("put: %+v %v", fi, err)
	}
	if b, err := c.GetFile(ctx, "dir/sub/all.bin"); err != nil || !bytes.Equal(b, all) {
		t.Fatalf("get: %v %v", b, err)
	}
	if _, err := c.PutFile(ctx, "b.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "b.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if b, _ := c.GetFile(ctx, "b.txt"); string(b) != "hi" {
		t.Fatalf("overwrite: %q", b)
	}
	if _, err := c.PutFile(ctx, "empty", nil); err != nil {
		t.Fatal(err)
	}
	want := []tiad.FileInfo{{Path: "b.txt", Size: 2}, {Path: "dir/sub/all.bin", Size: 256}, {Path: "empty", Size: 0}}
	if fs := files(t, c); !reflect.DeepEqual(fs, want) {
		t.Fatalf("list = %+v, want %+v", fs, want)
	}
	if err := c.DeleteFile(ctx, "b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetFile(ctx, "b.txt"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("get after delete: %v", err)
	}
	if err := c.DeleteFile(ctx, "b.txt"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := c.GetFile(ctx, "never"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("missing file: %v", err)
	}
}

// RefusedPaths break the sandbox rule but are clean URL paths, so the
// agent's own check must refuse them: 400, bad_request.
var RefusedPaths = []string{
	"NUL", "a/con.txt", "Com1.scl", "LPT9", "aux.txt.bak", "CON .txt",
	"COM0.txt", "lpt0", "COM¹", "lpt².txt", "a/Com³.scl", "LPT¹",
	"x.", "a/x./b", "dir /x", "trail ",
	"a|b", "a?b", "a*b", "a<b", "a>b", `a"b`,
	`a\b`, "C:/x", "c:x", "file.scl:stream",
	"a\x00b", "a\x01b", "a\x1fb", "a\x7fb",
}

// UncleanURLs reach outside the work dir, or name a path in a form that is
// not clean. An agent may refuse them however its HTTP stack likes (Go's
// router redirects), but never with a 2xx, and never writing anything.
var UncleanURLs = []string{
	"../x", "a/../../x", "a/../x", "./x", "a//x", "a/.", "a/./x",
	"a%2F..%2F..%2Fx", "%2e%2e/x", "%2E%2E%2Fx", "a%5C..%5C..%5Cx",
}

// AcceptedPaths obey the rule and must round-trip through the work dir.
var AcceptedPaths = []string{
	"a/b c.txt", "COM10", "COM", "lpt10", "COM⁴", "LPT₁", "con-x.txt", "CONSOLE", "nul_device", "a.b/c",
	"port-001/aux1", "ünï/çödé.txt", "x/.hidden", "100%", "a+b=c&d;e,f@g$h",
	"deep/er/than/you/think.scl",
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func contractPathsRefused(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	for _, p := range RefusedPaths {
		u := "/v1/files/" + escapePath(p)
		for _, method := range []string{"PUT", "GET", "DELETE"} {
			status, b := raw(t, c, method, u, []byte("x"))
			if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest {
				t.Errorf("%s %q = HTTP %d %s, want 400 bad_request", method, p, status, b)
			}
		}
	}
	for _, u := range UncleanURLs {
		for _, method := range []string{"PUT", "GET", "DELETE"} {
			if status, b := raw(t, c, method, "/v1/files/"+u, []byte("x")); status/100 == 2 {
				t.Errorf("%s %s = HTTP %d %s, want a refusal", method, u, status, b)
			}
		}
	}
	if fs := files(t, c); len(fs) != 0 {
		t.Fatalf("refused paths wrote %+v", fs)
	}
}

func contractPathsAccepted(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	for _, p := range AcceptedPaths {
		data := []byte("content of " + p)
		fi, err := c.PutFile(ctx, p, data)
		if err != nil || fi.Path != p || fi.Size != int64(len(data)) {
			t.Errorf("put %q: %+v %v", p, fi, err)
			continue
		}
		if b, err := c.GetFile(ctx, p); err != nil || !bytes.Equal(b, data) {
			t.Errorf("get %q: %q %v", p, b, err)
		}
	}
	got := map[string]bool{}
	for _, f := range files(t, c) {
		got[f.Path] = true
	}
	for _, p := range AcceptedPaths {
		if !got[p] {
			t.Errorf("list lacks %q (got %v)", p, got)
		}
	}
}
