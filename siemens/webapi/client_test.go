package webapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
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

func TestPermissionDeniedIsNotRetriedForever(t *testing.T) {
	ctx := context.Background()
	s, _, _ := cpu(t)
	url, pin := s.Start(t)
	v, _ := webapi.New(url, webapi.Options{User: "viewer", Password: "pw", CertSHA256: pin})
	before := s.Calls
	err := v.Write(ctx, `"Tags".Run`, true)
	if webapi.Code(err) != webapi.CodePermissionDenied {
		t.Fatalf("viewer write: %v", err)
	}
	// login, write (denied), login again, write (denied): no loop.
	if n := s.Calls - before; n != 4 {
		t.Fatalf("calls = %d, want 4", n)
	}
	if err := v.RequestOperatingMode(ctx, webapi.ModeStop); webapi.Code(err) != webapi.CodePermissionDenied {
		t.Fatalf("viewer stop: %v", err)
	}
}

func TestLoginFailures(t *testing.T) {
	ctx := context.Background()
	s, _, _ := cpu(t)
	url, pin := s.Start(t)
	bad, _ := webapi.New(url, webapi.Options{User: "naut", Password: "wrong", CertSHA256: pin})
	if err := bad.Login(ctx); webapi.Code(err) != webapi.CodeLoginFailed {
		t.Fatalf("wrong password: %v", err)
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
	unpinned, _ := webapi.New(url, webapi.Options{})
	if _, err := unpinned.Version(ctx); err == nil {
		t.Fatal("a self-signed CPU must not be trusted without a pin")
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
	s, _, _ := cpu(t)
	var vars []string
	for i := range 600 {
		sym := fmt.Sprintf(`"Tags".V%03d`, i)
		s.Vars[sym] = &webapitest.Var{Datatype: "int", Value: i}
		vars = append(vars, sym)
	}
	vars = append(vars, `"Tags".Missing`)
	s.MaxBody = 8 * 1024
	url, pin := s.Start(t)
	c, _ := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin, MaxBody: 8 * 1024})
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
