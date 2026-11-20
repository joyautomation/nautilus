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
	// SetCompileResult scripts what compile returns. Zero means success;
	// a result with errors makes compile fail with compile_failed.
	SetCompileResult(t testing.TB, r tiad.CompileResult)
	// SetQuestions scripts TIA's pre-download checks for every download.
	SetQuestions(t testing.TB, qs []tiad.Question)
	// Loaded lists every download that loaded the CPU.
	Loaded(t testing.TB) []Loaded
	// SetDevice scripts the CPU's identity, as an upload and an online
	// read report it, and with noIdentify a TIA side that cannot read it
	// online (tiad.Openness today).
	SetDevice(t testing.TB, d tiad.DeviceIdentity, noIdentify bool)
}

// Loaded is one download that loaded a CPU.
type Loaded struct {
	Address            string
	Hardware, Software bool
	State              string
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
func (g goFake) SetCompileResult(_ testing.TB, r tiad.CompileResult) {
	g.f.SetCompileResult(r)
}
func (g goFake) SetQuestions(_ testing.TB, qs []tiad.Question) { g.f.SetQuestions(qs) }
func (g goFake) SetDevice(_ testing.TB, d tiad.DeviceIdentity, noIdentify bool) {
	g.f.SetDevice(d, noIdentify)
}
func (g goFake) Loaded(testing.TB) []Loaded {
	var out []Loaded
	for _, d := range g.f.Downloads() {
		out = append(out, Loaded{Address: d.Request.Address, Hardware: d.Request.Hardware, Software: d.Request.Software, State: d.State})
	}
	return out
}

// Contract runs the agent's HTTP contract (docs/design/siemens-ide.md §3)
// against the agents start makes, which must be scriptable (a fake).
func Contract(t *testing.T, start Starter) {
	ContractNoTIA(t, start)
	t.Run("Probe", func(t *testing.T) { contractProbe(t, start) })
	t.Run("GateStates", func(t *testing.T) { contractGateStates(t, start) })
	contractTIA(t, start)
	contractDownload(t, start)
	t.Run("Pins", func(t *testing.T) { contractPins(t, start) })
}

// ContractNoTIA is the part of the contract that never needs TIA and
// scripts nothing: health, the token, the browser and content-type
// refusals, the files and the path rules. It runs against the real agent
// on a TIA machine before TIA may start (the trial clock), as well as
// against the fakes.
func ContractNoTIA(t *testing.T, start Starter) {
	t.Run("Health", func(t *testing.T) { contractHealth(t, start) })
	t.Run("Token", func(t *testing.T) { contractToken(t, start) })
	t.Run("NoBrowser", func(t *testing.T) { contractNoBrowser(t, start) })
	t.Run("ContentType", func(t *testing.T) { contractContentType(t, start) })
	t.Run("Files", func(t *testing.T) { contractFiles(t, start) })
	t.Run("PathsRefused", func(t *testing.T) { contractPathsRefused(t, start) })
	t.Run("PathsAccepted", func(t *testing.T) { contractPathsAccepted(t, start) })
}

// GateOrder is the gates probe reports, in order.
const GateOrder = "tia,openness-group,openness-allowlist,licence,workdir"

// CheckGateShape fails t unless p reports exactly the gates in GateOrder,
// each in a known state with OK true exactly in state ok, and Usable
// exactly when no gate blocks. It scripts nothing, so it holds for the
// real agent too.
func CheckGateShape(t testing.TB, p tiad.Probe) {
	t.Helper()
	if got := gateNames(p.Gates); got != GateOrder {
		t.Fatalf("probe gates %s, want %s", got, GateOrder)
	}
	blocked := false
	for _, g := range p.Gates {
		switch g.State {
		case tiad.GateOK, tiad.GateFailed, tiad.GateUnknown:
		default:
			t.Errorf("gate %s: state %q, want ok, failed or unknown", g.Name, g.State)
		}
		if g.OK != (g.State == tiad.GateOK) {
			t.Errorf("gate %s: ok %v in state %q", g.Name, g.OK, g.State)
		}
		if g.State != tiad.GateOK && g.Detail == "" {
			t.Errorf("gate %s: state %q without a detail", g.Name, g.State)
		}
		blocked = blocked || g.Blocks()
	}
	if p.Usable == blocked {
		t.Errorf("probe usable %v, but a gate blocks: %v", p.Usable, blocked)
	}
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
	CheckGateShape(t, p)
	if !p.Usable {
		t.Fatalf("probe = %+v, want usable", p)
	}
	for _, g := range p.Gates {
		if g.State != tiad.GateOK {
			t.Errorf("gate %s is %s: %s", g.Name, g.State, g.Detail)
		}
	}

	// A failed gate is a successful probe that says "not usable".
	a.SetGates(t, []tiad.Gate{{Name: "licence", State: tiad.GateFailed, Detail: "trial expired"}})
	p, err = c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe with a failed gate: %v", err)
	}
	failed := false
	for _, g := range p.Gates {
		failed = failed || g.Name == "licence" && !g.OK && g.State == tiad.GateFailed && g.Detail == "trial expired"
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

// contractGateStates: an unknown gate (the licence before TIA's first
// start) is reported, is not OK, and blocks nothing; a state the agent
// does not know blocks, as a failed one does.
func contractGateStates(t *testing.T, start Starter) {
	a := start(t, Config{})
	c := a.Client()
	if _, err := c.PutFile(ctx, "gsd/"+benchGSDML, []byte("<ISO15745Profile/>")); err != nil {
		t.Fatal(err)
	}
	licence := func(p tiad.Probe) tiad.Gate {
		for _, g := range p.Gates {
			if g.Name == "licence" {
				return g
			}
		}
		t.Fatalf("probe has no licence gate: %+v", p)
		return tiad.Gate{}
	}

	// ok in the JSON is derived from the state, in the fakes as in the
	// agent: scripting a state is enough.
	a.SetGates(t, []tiad.Gate{{Name: "licence", State: tiad.GateOK, Detail: "licensed"}})
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := licence(p); !g.OK || !p.Usable {
		t.Fatalf("licence ok: probe = %+v, want the gate ok and usable", p)
	}

	const why = "not readable until TIA's first start"
	a.SetGates(t, []tiad.Gate{{Name: "licence", State: tiad.GateUnknown, Detail: why}})
	p, err = c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := licence(p); g.State != tiad.GateUnknown || g.OK || g.Detail != why || !p.Usable {
		t.Fatalf("unknown licence: probe = %+v, want the gate unknown, not ok, and usable", p)
	}
	if _, err := c.InstallGSD(ctx, "gsd/"+benchGSDML); err != nil {
		t.Fatalf("a TIA call with an unknown gate: %v, want it to run", err)
	}

	a.SetGates(t, []tiad.Gate{{Name: "licence", State: "bogus", Detail: "a state from a newer agent"}})
	if p, err = c.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if g := licence(p); g.OK || p.Usable {
		t.Fatalf("unknown state: probe = %+v, want not usable", p)
	}
	_, err = c.InstallGSD(ctx, "gsd/"+benchGSDML)
	var te *tiad.Error
	if !errorsAs(err, &te) || te.Kind != tiad.KindGate || !strings.Contains(te.Message, "licence") {
		t.Fatalf("a TIA call with a gate in an unknown state: %v, want 412 gate", err)
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

// hasControl reports whether p holds an ASCII control character.
func hasControl(p string) bool {
	return strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func contractPathsRefused(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	for _, p := range RefusedPaths {
		u := "/v1/files/" + escapePath(p)
		for _, method := range []string{"PUT", "GET", "DELETE"} {
			status, b := raw(t, c, method, u, []byte("x"))
			kind := envelopeKind(b)
			// Windows's HTTP stack (http.sys) refuses a control character
			// in a URL itself, with its own 400, before the agent sees the
			// request. Refused all the same, so only there is a 400 that
			// is not the agent's envelope accepted.
			if status == http.StatusBadRequest && kind == "" && hasControl(p) {
				continue
			}
			if status != http.StatusBadRequest || kind != tiad.KindBadRequest {
				t.Errorf("%s %q = HTTP %d %s, want 400 bad_request", method, p, status, b)
			}
		}
	}
	if fs := files(t, c); len(fs) != 0 {
		t.Errorf("refused paths wrote %v", fs)
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
