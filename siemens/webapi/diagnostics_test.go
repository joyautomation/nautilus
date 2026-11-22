package webapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

// diagCPU is the fake CPU with a three-entry diagnostic buffer, newest
// first, and a user "diag" who may read it. The texts are synthetic.
func diagCPU(t *testing.T, setup ...func(*webapitest.Server)) (*webapitest.Server, *webapi.Client, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	s := webapitest.New()
	s.Now = clk.now
	s.Users["diag"] = webapitest.User{Password: "pw", Permissions: []string{"read_diagnostics"}}
	t0 := time.Date(2026, 11, 17, 11, 0, 0, 511854547, time.UTC)
	for _, e := range []webapitest.DiagEntry{
		{Time: t0, TextList: 2, Text: 16385, Short: "Boot up", Long: "CPU changes from OFF to STOP"},
		{Time: t0.Add(time.Minute), Incoming: true, TextList: 2, Text: 100, Short: "Mode change", Long: "STOP to RUN", Help: "none"},
		{Time: t0.Add(2 * time.Minute), Incoming: true, TextList: 9, Text: 7, Short: "Download", Long: "Blocks loaded", Help: "help"},
	} {
		s.AddDiag(e)
	}
	for _, f := range setup {
		f(s)
	}
	url, pin := s.Start(t)
	c, err := webapi.New(url, webapi.Options{User: "diag", Password: "pw", CertSHA256: pin, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	return s, c, clk
}

func TestDiagnosticBufferReadsEntriesAsTheCPUSendsThem(t *testing.T) {
	_, c, _ := diagCPU(t)
	b, err := c.DiagnosticBuffer(context.Background(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if b.Count != 3 || b.Max != 3200 || b.Language != "en-US" || len(b.Entries) != 2 {
		t.Fatalf("buffer: %+v", b)
	}
	e := b.Entries[0]
	if e.Status != webapi.DiagIncoming || e.Event != (webapi.DiagEvent{TextList: 9, Text: 7}) ||
		e.ShortText != "Download" || e.LongText != "Blocks loaded" || e.HelpText != "help" {
		t.Fatalf("newest entry: %+v", e)
	}
	if b.Entries[1].ShortText != "Mode change" {
		t.Fatalf("entries are not in the CPU's order: %+v", b.Entries)
	}
	at, err := e.Time()
	want := time.Date(2026, 11, 17, 11, 2, 0, 511854547, time.UTC)
	if err != nil || !at.Equal(want) {
		t.Fatalf("time %q -> %v %v, want %v", e.Timestamp, at, err, want)
	}
	if b.LastModified != "2026-11-17T11:02:00.511854547Z" {
		t.Fatalf("last_modified %q", b.LastModified)
	}
}

// count 0 must reach the CPU: it means "status only", and leaving it out
// means the default of 50 entries.
func TestDiagnosticBufferStatusSendsCountZero(t *testing.T) {
	s, c, _ := diagCPU(t)
	ctx := context.Background()
	b, err := c.DiagnosticBufferStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Entries) != 0 || b.Language != "" || b.Count != 3 || b.Max != 3200 {
		t.Fatalf("status read: %+v", b)
	}
	s.AddDiag(webapitest.DiagEntry{Time: time.Date(2026, 11, 17, 11, 5, 0, 0, time.UTC), Incoming: true, Short: "x"})
	b2, err := c.DiagnosticBufferStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Count != 4 || b2.LastModified == b.LastModified {
		t.Fatalf("a new entry must change the status: before %+v after %+v", b, b2)
	}
}

func TestDiagnosticBufferNeedsReadDiagnostics(t *testing.T) {
	s, c, _ := cpu(t) // "naut" may read and write values, not diagnostics
	_, err := c.DiagnosticBuffer(context.Background(), "en-US", 10)
	if webapi.Code(err) != webapi.CodePermissionDenied {
		t.Fatalf("want permission denied, got %v", err)
	}
	if s.Logins != 1 {
		t.Fatalf("a genuine denial must not log in again: %d logins", s.Logins)
	}
}

func TestDiagnosticBufferOnFirmwareWithoutIt(t *testing.T) {
	_, c, _ := diagCPU(t, func(s *webapitest.Server) {
		s.Methods = []string{"Api.Login", "Api.Logout", "Api.Ping", "Api.GetPermissions"}
	})
	_, err := c.DiagnosticBuffer(context.Background(), "en-US", 10)
	if webapi.Code(err) != webapi.CodeMethodNotFound {
		t.Fatalf("want method not found, got %v", err)
	}
}

func TestDiagnosticBufferRefusesBadArgumentsBeforeCalling(t *testing.T) {
	s, c, _ := diagCPU(t)
	ctx := context.Background()
	for _, n := range []int{0, -1} {
		if _, err := c.DiagnosticBuffer(ctx, "en-US", n); err == nil {
			t.Fatalf("count %d accepted", n)
		}
	}
	for _, l := range []string{" en-US", "en US", `en"US`} {
		if _, err := c.DiagnosticBuffer(ctx, l, 5); err == nil {
			t.Fatalf("language %q accepted", l)
		}
	}
	if s.Calls != 0 {
		t.Fatalf("%d calls reached the CPU", s.Calls)
	}
}

// The fake refuses what a CPU is expected to refuse, so a client that
// drops the language or sends an unmodelled filter goes red here.
func TestFakeDiagnosticBufferParams(t *testing.T) {
	_, c, _ := diagCPU(t)
	ctx := context.Background()
	for name, params := range map[string]any{
		"no params":      nil,
		"no language":    map[string]any{"count": 5},
		"empty language": map[string]any{"language": "", "count": 5},
		"negative count": map[string]any{"language": "en-US", "count": -1},
		"filters":        map[string]any{"language": "en-US", "filters": map[string]any{"mode": "include", "attributes": []string{"short_text"}}},
	} {
		rs, err := c.Do(ctx, webapi.Call{Method: "DiagnosticBuffer.Browse", Params: params})
		if err != nil {
			t.Fatal(err)
		}
		if webapi.Code(rs[0].Err) != webapi.CodeInvalidParams {
			t.Fatalf("%s: want invalid params, got %v", name, rs[0].Err)
		}
	}
	// Without count the CPU's default is 50; this buffer holds 3.
	rs, err := c.Do(ctx, webapi.Call{Method: "DiagnosticBuffer.Browse", Params: map[string]any{"language": "en-US"}})
	if err != nil || rs[0].Err != nil {
		t.Fatal(err, rs[0].Err)
	}
}
