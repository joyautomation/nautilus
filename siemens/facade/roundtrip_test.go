package facade

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
	"github.com/joyautomation/nautilus/siemens/writer"
)

// TestWriterAndFacadeAgreeOnNames is the round trip: siemens/writer
// generates a project and names each value's Web API symbol
// (Output.Symbol); a CPU holding those DBs is browsed from the writer's own
// roots (RootsFromWriter); every name serve binds must map, through the
// writer, to the very symbol serve reads. Discover derives names from the
// browse alone, so a drift on either side breaks this.
func TestWriterAndFacadeAgreeOnNames(t *testing.T) {
	src := "PROGRAM Main\nVAR count : INT; run : BOOL; dwell : TON; END_VAR\ncount := 1;\nrun := Start;\nEND_PROGRAM\n"
	out, diags, err := writer.Write(writer.Input{
		Programs: []writer.Program{{Path: "Main.st", Source: src}},
		Globals:  []writer.Global{{Name: "Level", Type: "REAL"}, {Name: "Start", Type: "BOOL"}},
		Tasks:    []writer.Task{{Name: "main", Program: "Main", Interval: time.Second}},
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}

	// The CPU after the download: what the writer's blocks hold.
	s := webapitest.New()
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	put := func(path, datatype string, v any) {
		sym, ok := out.Symbol(path)
		if !ok {
			t.Fatalf("the writer has no symbol for %s", path)
		}
		s.Vars[sym] = &webapitest.Var{Datatype: datatype, Value: v}
	}
	for _, db := range out.Roots {
		s.Vars[db] = &webapitest.Var{Datatype: "struct"}
	}
	put("Level", "real", 1.5)
	put("Start", "bool", true)
	put("Main.count", "int", 1)
	put("Main.run", "bool", true)
	put("Main.dwell", "struct", nil)
	put("Main.dwell.Q", "bool", false)
	url, pin := s.Start(t)
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}

	bs, _, err := Discover(context.Background(), c, RootsFromWriter(out.Roots))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, b := range bs {
		seen[b.Name] = true
		want, ok := out.Symbol(b.Name)
		if !ok || want != b.Symbol {
			t.Errorf("serve binds %s to %s, the writer maps it to %q (%v)", b.Name, b.Symbol, want, ok)
		}
	}
	for _, name := range []string{"Level", "Start", "Main.count", "Main.run", "Main.dwell.Q"} {
		if !seen[name] {
			t.Errorf("serve did not bind %s (bound %v)", name, bs)
		}
	}
}

func TestRootsFromWriterOrder(t *testing.T) {
	got := RootsFromWriter(map[string]string{"": `"Tags"`, "Zeta": `"Zeta_DB"`, "Alpha": `"Alpha_DB"`})
	want := []Root{{DB: "Tags"}, {DB: "Alpha_DB", Prefix: "Alpha"}, {DB: "Zeta_DB", Prefix: "Zeta"}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%+v, want %+v", got, want)
		}
	}
}

// TestBackoffIsCapped: repeated failures double the wait up to BackoffMax
// and no further.
func TestBackoffIsCapped(t *testing.T) {
	s, c := cpu(t)
	d := NewDriver(c, []Binding{{Name: "Level", Symbol: `"Tags".Level`, Type: "REAL"}}, time.Second)
	// 3 s doubles past the 8 s cap (3, 6, 12): the cap must clamp it.
	d.BackoffMin, d.BackoffMax = 3*time.Second, 8*time.Second
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	s.StopAnswering()
	want := []time.Duration{3, 6, 8, 8, 8}
	for i, w := range want {
		d.PollOnce(context.Background())
		if d.backoff != w*time.Second {
			t.Fatalf("failure %d: backoff %s, want %s", i+1, d.backoff, w*time.Second)
		}
	}
}

// TestAPITokenSurvivesFacadeSetup: New replaces the server options'
// TagWriter and Drivers, and must keep the rest, the bearer token first.
func TestAPITokenSurvivesFacadeSetup(t *testing.T) {
	s, c := cpu(t)
	f, err := New(context.Background(), Options{Client: c, Host: "cpu", Roots: roots, Poll: 20 * time.Millisecond,
		Server: server.Options{AuthToken: "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	api := http.HandlerFunc(f.Handler().ServeHTTP)
	write := func(auth string, v float64) int {
		req, _ := http.NewRequest(http.MethodPost, "/api/tags", strings.NewReader(`{"name":"Level","value":`+strconvF(v)+`}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := &recorder{header: http.Header{}}
		api.ServeHTTP(rec, req)
		return rec.code
	}
	for _, auth := range []string{"", "Bearer wrong"} {
		if code := write(auth, 2.5); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("auth %q: %d, want a refusal (401/403)", auth, code)
		}
	}
	if s.Value(`"Tags".Level`) != 12.5 {
		t.Fatal("a write without the token reached the CPU")
	}
	if code := write("Bearer secret-token", 3.5); code != http.StatusNoContent {
		t.Fatalf("with the token: %d, want 204", code)
	}
	if s.Value(`"Tags".Level`) != 3.5 {
		t.Fatal("the authorized write did not reach the CPU")
	}
}

func strconvF(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

type recorder struct {
	header http.Header
	code   int
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return len(b), nil
}
func (r *recorder) WriteHeader(c int) {
	if r.code == 0 {
		r.code = c
	}
}
