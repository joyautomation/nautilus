package webapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func cpu(t *testing.T) (*webapitest.Server, *webapi.Client, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	s := webapitest.New()
	s.Now = clk.now
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value", "write_value", "change_operating_mode"}}
	s.Users["viewer"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	s.Vars[`"Tags"`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"Tags".Level`] = &webapitest.Var{Datatype: "real", Value: 12.5}
	s.Vars[`"Tags".Run`] = &webapitest.Var{Datatype: "bool", Value: false}
	s.Vars[`"Tags".Count`] = &webapitest.Var{Datatype: "dint", Value: 7, ReadOnly: true}
	url, pin := s.Start(t)
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	return s, c, clk
}

func TestAnonymousCalls(t *testing.T) {
	ctx := context.Background()
	s, c, _ := cpu(t)
	if v, err := c.Version(ctx); err != nil || v != "4" {
		t.Fatalf("version: %q %v", v, err)
	}
	ms, err := c.Methods(ctx)
	if err != nil || !contains(ms, "PlcProgram.Read") {
		t.Fatalf("methods: %v %v", ms, err)
	}
	p1, err := c.Ping(ctx)
	if err != nil || p1 == "" {
		t.Fatalf("ping: %q %v", p1, err)
	}
	if s.Sessions() != 0 {
		t.Fatal("anonymous calls must not log in")
	}
}

func TestReadWriteBrowse(t *testing.T) {
	ctx := context.Background()
	s, c, _ := cpu(t)
	raw, err := c.Read(ctx, `"Tags".Level`)
	if err != nil || string(raw) != "12.5" {
		t.Fatalf("read: %s %v", raw, err)
	}
	if err := c.Write(ctx, `"Tags".Run`, true); err != nil {
		t.Fatal(err)
	}
	if s.Value(`"Tags".Run`) != true {
		t.Fatal("write did not land")
	}
	if err := c.Write(ctx, `"Tags".Count`, 1); webapi.Code(err) != webapi.CodeAddressIsReadOnly {
		t.Fatalf("read-only write: %v", err)
	}
	if _, err := c.Read(ctx, `"Tags".Nope`); webapi.Code(err) != webapi.CodeAddressDoesNotExist || !strings.Contains(err.Error(), "address does not exist") {
		t.Fatalf("missing var: %v", err)
	}
	vs, err := c.Browse(ctx, `"Tags"`, false)
	if err != nil || len(vs) != 3 || vs[0].Name != "Count" || vs[1].Datatype != "real" {
		t.Fatalf("browse: %+v %v", vs, err)
	}
	if _, err := c.Browse(ctx, `"Tags".Level`, false); webapi.Code(err) != webapi.CodeNotAStructure {
		t.Fatalf("browse a leaf: %v", err)
	}
	if s.Sessions() != 1 {
		t.Fatalf("sessions = %d, want one login reused", s.Sessions())
	}
}

func TestExpiredTokenLogsInAgainOnce(t *testing.T) {
	ctx := context.Background()
	s, c, clk := cpu(t)
	if _, err := c.Read(ctx, `"Tags".Level`); err != nil {
		t.Fatal(err)
	}
	clk.add(121 * time.Second)
	if _, err := c.Read(ctx, `"Tags".Level`); err != nil {
		t.Fatalf("read after the token expired: %v", err)
	}
	s.Expire() // a CPU restart
	if _, err := c.Read(ctx, `"Tags".Level`); err != nil {
		t.Fatalf("read after a restart: %v", err)
	}
}

func TestGenuineDenialKeepsTheToken(t *testing.T) {
	ctx := context.Background()
	s, _, clk := cpu(t)
	url, pin := s.Start(t)
	v, _ := webapi.New(url, webapi.Options{User: "viewer", Password: "pw", CertSHA256: pin, Now: clk.now})
	for range 3 {
		if err := v.Write(ctx, `"Tags".Run`, true); webapi.Code(err) != webapi.CodePermissionDenied {
			t.Fatalf("viewer write: %v", err)
		}
	}
	if err := v.RequestOperatingMode(ctx, webapi.ModeStop); webapi.Code(err) != webapi.CodePermissionDenied {
		t.Fatalf("viewer stop: %v", err)
	}
	// Denials are not expiry: one session, still usable.
	if n := s.Sessions(); n != 1 {
		t.Fatalf("sessions = %d after four denials, want 1", n)
	}
	if _, err := v.Read(ctx, `"Tags".Level`); err != nil {
		t.Fatal(err)
	}
	if n := s.Sessions(); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
}

func TestConcurrentFirstCallsShareOneLogin(t *testing.T) {
	ctx := context.Background()
	s, c, _ := cpu(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Read(ctx, `"Tags".Level`); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := s.Sessions(); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
}

func TestLoginFailures(t *testing.T) {
	ctx := context.Background()
	s, _, _ := cpu(t)
	url, pin := s.Start(t)
	bad, _ := webapi.New(url, webapi.Options{User: "naut", Password: "wrong", CertSHA256: pin})
	before := s.Calls
	if err := bad.Login(ctx); webapi.Code(err) != webapi.CodeLoginFailed {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := bad.Read(ctx, `"Tags".Level`); webapi.Code(err) != webapi.CodeLoginFailed {
		t.Fatalf("read with a wrong password: %v", err)
	}
	if n := s.Calls - before; n != 2 {
		t.Fatalf("calls = %d, want 2 (one failed login each, no loop)", n)
	}
	none, _ := webapi.New(url, webapi.Options{CertSHA256: pin})
	if _, err := none.Read(ctx, `"Tags".Level`); err == nil || !strings.Contains(err.Error(), "no user") {
		t.Fatalf("no user: %v", err)
	}
	s.MaxSessions = 1
	a, _ := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
	b, _ := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
	if err := a.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Login(ctx); webapi.Code(err) != webapi.CodeNoResources {
		t.Fatalf("session limit: %v", err)
	}
	if err := a.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Login(ctx); err != nil {
		t.Fatalf("after logout frees a slot: %v", err)
	}
}

func TestPinnedCertificate(t *testing.T) {
	ctx := context.Background()
	s, _, _ := cpu(t)
	url, pin := s.Start(t)
	wrong := strings.Repeat("ab", 32)
	c, _ := webapi.New(url, webapi.Options{CertSHA256: wrong})
	if _, err := c.Version(ctx); err == nil || !strings.Contains(err.Error(), "does not match the pinned") {
		t.Fatalf("wrong pin: %v", err)
	}
	if _, err := webapi.New(url, webapi.Options{}); err == nil || !strings.Contains(err.Error(), "no way to trust") {
		t.Fatalf("no pin and no CA must be refused: %v", err)
	}
	for _, u := range []string{"http://192.0.2.10", "HTTP://192.0.2.10", "ftp://x"} {
		if _, err := webapi.New(u, webapi.Options{CertSHA256: pin}); err == nil || !strings.Contains(err.Error(), "only https") {
			t.Errorf("New(%q) = %v, want only-https", u, err)
		}
	}
	got, err := webapi.CertificateSHA256(ctx, strings.TrimPrefix(url, "https://"))
	if err != nil || got != pin {
		t.Fatalf("CertificateSHA256 = %s %v, want %s", got, err, pin)
	}
	if _, err := webapi.New(url, webapi.Options{CertSHA256: "nothex"}); err == nil {
		t.Fatal("a malformed pin must be refused")
	}
}

func TestReadManySplitsUnderTheBodyLimit(t *testing.T) {
	ctx := context.Background()
	s, _, clk := cpu(t)
	var vars []string
	for i := range 600 {
		sym := fmt.Sprintf(`"Tags".V%03d`, i)
		s.Vars[sym] = &webapitest.Var{Datatype: "int", Value: i}
		vars = append(vars, sym)
	}
	vars = append(vars, `"Tags".Missing`)
	s.MaxBody = 8 * 1024
	url, pin := s.Start(t)
	c, _ := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin, MaxBody: 8 * 1024, Now: clk.now})
	before := s.Requests
	rs, err := c.ReadMany(ctx, vars)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != len(vars) {
		t.Fatalf("results = %d", len(rs))
	}
	for i := range 600 {
		var n int
		if rs[i].Err != nil || json.Unmarshal(rs[i].Value, &n) != nil || n != i {
			t.Fatalf("result %d: %s %v", i, rs[i].Value, rs[i].Err)
		}
	}
	if webapi.Code(rs[600].Err) != webapi.CodeAddressDoesNotExist {
		t.Fatalf("missing var in a batch fails alone: %v", rs[600].Err)
	}
	if n := s.Requests - before; n < 3 || n > 12 {
		t.Fatalf("requests = %d: batching should split 601 reads into a handful", n)
	}
}

func TestWriteManySendsNothingWhenAValueCannotBeEncoded(t *testing.T) {
	ctx := context.Background()
	s, c, _ := cpu(t)
	before := s.Calls
	rs, err := c.WriteMany(ctx, []webapi.Write{{`"Tags".Run`, true}, {`"Tags".Level`, math.NaN()}})
	if err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("NaN: %v", err)
	}
	if len(rs) != 2 || rs[0].Var != `"Tags".Run` || !errors.Is(rs[0].Err, webapi.ErrNotSent) || !errors.Is(rs[1].Err, webapi.ErrNotSent) {
		t.Fatalf("every result must say not sent: %+v", rs)
	}
	if s.Calls != before || s.Value(`"Tags".Run`) != false {
		t.Fatal("a bad value must stop the whole batch before anything is sent")
	}
}

func TestWriteManyReportsWhatWasSentWhenARequestFails(t *testing.T) {
	ctx := context.Background()
	s, _, clk := cpu(t)
	var ws []webapi.Write
	for i := range 300 {
		sym := fmt.Sprintf(`"Tags".W%03d`, i)
		s.Vars[sym] = &webapitest.Var{Datatype: "int", Value: 0}
		ws = append(ws, webapi.Write{Var: sym, Value: i})
	}
	s.MaxBody = 4 * 1024
	url, pin := s.Start(t)
	c, _ := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin, MaxBody: 4 * 1024, Now: clk.now})
	if _, err := c.Read(ctx, `"Tags".Level`); err != nil { // log in first
		t.Fatal(err)
	}
	s.FailAfter = s.Requests + 1 // the first write request lands, the second is lost
	rs, err := c.WriteMany(ctx, ws)
	if err == nil || len(rs) != len(ws) {
		t.Fatalf("want every result and an error: %d %v", len(rs), err)
	}
	var landed, unknown, notSent int
	for _, r := range rs {
		switch {
		case r.Err == nil:
			landed++
		case errors.Is(r.Err, webapi.ErrUnknown):
			unknown++
		case errors.Is(r.Err, webapi.ErrNotSent):
			notSent++
		default:
			t.Fatalf("%s: %v", r.Var, r.Err)
		}
	}
	if landed == 0 || unknown == 0 || notSent == 0 || landed+unknown+notSent != len(ws) {
		t.Fatalf("landed %d unknown %d not sent %d", landed, unknown, notSent)
	}
	if got := s.Value(rs[landed-1].Var); got != float64(landed-1) {
		t.Fatalf("last landed write: %v", got)
	}
}

func TestFakeRefusesWrongTypes(t *testing.T) {
	ctx := context.Background()
	s, c, _ := cpu(t)
	s.Vars[`"Tags".Name`] = &webapitest.Var{Datatype: "string", Value: "", MaxLength: 4}
	s.Vars[`"Tags".Small`] = &webapitest.Var{Datatype: "sint", Value: 0}
	s.Vars[`"Tags".T`] = &webapitest.Var{Datatype: "time", Value: 0}
	for _, w := range []webapi.Write{
		{`"Tags".Level`, "high"},
		{`"Tags".Run`, 1},
		{`"Tags".Small`, 200},
		{`"Tags".Small`, 1.5},
		{`"Tags".Name`, "toolong"},
		{`"Tags".T`, 1000},
	} {
		if err := c.Write(ctx, w.Var, w.Value); webapi.Code(err) != webapi.CodeInvalidParams {
			t.Errorf("write %s = %v: %v, want invalid params", w.Var, w.Value, err)
		}
	}
	if err := c.Write(ctx, `"Tags".Name`, "abcd"); err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, `"Tags".Small`, -128); err != nil {
		t.Fatal(err)
	}
}

func TestWriteManyAndMode(t *testing.T) {
	ctx := context.Background()
	s, c, _ := cpu(t)
	rs, err := c.WriteMany(ctx, []webapi.Write{{`"Tags".Run`, true}, {`"Tags".Count`, 3}, {`"Tags".Level`, 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].Err != nil || webapi.Code(rs[1].Err) != webapi.CodeAddressIsReadOnly || rs[2].Err != nil {
		t.Fatalf("write many: %+v", rs)
	}
	if m, err := c.OperatingMode(ctx); err != nil || m != webapi.ModeRun {
		t.Fatalf("mode: %q %v", m, err)
	}
	if err := c.RequestOperatingMode(ctx, webapi.ModeStop); err != nil || s.Mode != "stop" {
		t.Fatalf("stop: %v %s", err, s.Mode)
	}
	if err := c.RequestOperatingMode(ctx, "hold"); err == nil {
		t.Fatal("only run and stop can be requested")
	}
}

func TestSymbol(t *testing.T) {
	for _, c := range []struct {
		root string
		mem  []string
		want string
	}{
		{"Tags", []string{"Pump", "Speed"}, `"Tags".Pump.Speed`},
		{"Main_DB", []string{"ton 1"}, `"Main_DB"."ton 1"`},
		{"Start", nil, `"Start"`},
	} {
		got, err := webapi.Symbol(c.root, c.mem...)
		if err != nil || got != c.want {
			t.Errorf("Symbol(%q, %q) = %q %v, want %q", c.root, c.mem, got, err, c.want)
		}
	}
	if _, err := webapi.Symbol(`a"b`); err == nil {
		t.Error("a quote in a name must be refused")
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func TestOversizedCallMarksEveryResultNotSent(t *testing.T) {
	ctx := context.Background()
	s, c, _ := cpu(t)
	before := s.Calls
	long := strings.Repeat("x", 70*1024)
	rs, err := c.WriteMany(ctx, []webapi.Write{{`"Tags".Run`, true}, {`"Tags".Name`, long}})
	if err == nil || len(rs) != 2 {
		t.Fatalf("want an error and two results: %v %d", err, len(rs))
	}
	for _, r := range rs {
		if r.Var == "" || !errors.Is(r.Err, webapi.ErrNotSent) {
			t.Fatalf("result %+v: want its Var and ErrNotSent", r)
		}
	}
	if s.Calls != before {
		t.Fatal("nothing may be sent")
	}
	rr, err := c.ReadMany(ctx, []string{`"Tags".Run`, `"` + long + `"`})
	if err == nil || len(rr) != 2 || rr[0].Var != `"Tags".Run` || !errors.Is(rr[1].Err, webapi.ErrNotSent) {
		t.Fatalf("ReadMany: %v %+v", err, rr)
	}
}

// TestRedirectIsNeverFollowed: a 307 from the CPU's address would re-send
// Api.Login's body, the password, to wherever it points.
func TestRedirectIsNeverFollowed(t *testing.T) {
	ctx := context.Background()
	var leaked atomic.Bool
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer plain.Close()
	redir := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/api/jsonrpc", http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	sum := sha256.Sum256(redir.Certificate().Raw)
	c, err := webapi.New(redir.URL, webapi.Options{User: "naut", Password: "pw", CertSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(ctx); err == nil {
		t.Fatal("a redirect must not be a successful login")
	}
	if leaked.Load() {
		t.Fatal("the login was re-sent to the redirect target")
	}
}

// TestCallInFlightDuringReloginRetriesWithTheNewToken: a call that was sent
// on the old token while another call logged in again must not report a
// PermissionDenied that belongs to the old token.
func TestCallInFlightDuringReloginRetriesWithTheNewToken(t *testing.T) {
	ctx := context.Background()
	s, c, clk := cpu(t)
	if _, err := c.Read(ctx, `"Tags".Level`); err != nil {
		t.Fatal(err)
	}
	clk.add(121 * time.Second) // the token expires
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Read(ctx, `"Tags".Level`); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("read during re-login: %v", err)
	}
	if n := s.Sessions(); n > 2 {
		t.Fatalf("sessions = %d", n)
	}
}

// TestMissingBulkReplyIsUnknown: a bulk response that leaves out one
// call's reply makes that call's outcome unknown, not a success and not
// a plain failure.
func TestMissingBulkReplyIsUnknown(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqs []map[string]any
		body, _ := io.ReadAll(r.Body)
		if json.Unmarshal(body, &reqs) != nil { // a single call: the login
			var one map[string]any
			json.Unmarshal(body, &one)
			res := any("ping")
			if one["method"] == "Api.Login" {
				res = map[string]string{"token": "t"}
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": one["id"], "result": res})
			return
		}
		// Answer every call but the last.
		var out []map[string]any
		for _, q := range reqs[:len(reqs)-1] {
			out = append(out, map[string]any{"jsonrpc": "2.0", "id": q["id"], "result": true})
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	c, err := webapi.New(srv.URL, webapi.Options{User: "naut", Password: "pw", CertSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := c.WriteMany(ctx, []webapi.Write{{`"Tags".A`, 1}, {`"Tags".B`, 2}, {`"Tags".C`, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].Err != nil || rs[1].Err != nil {
		t.Fatalf("answered calls: %+v", rs[:2])
	}
	if !errors.Is(rs[2].Err, webapi.ErrUnknown) {
		t.Fatalf("the unanswered call: %v, want ErrUnknown", rs[2].Err)
	}
}
