package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

func TestSiemensDiagRefusesWhatIsMissing(t *testing.T) {
	none := t.TempDir()
	for _, c := range []struct {
		name, host, user, pin string
		count                 int
		status                bool
		getenv                func(string) string
		want                  string
	}{
		{"no host", "", "naut", "ab", 50, false, pw, "--host is required"},
		{"no user", "192.0.2.10", "", "ab", 50, false, pw, "--user is required"},
		{"no password", "192.0.2.10", "naut", "ab", 50, false, env(nil), "NAUTILUS_SIEMENS_PASSWORD"},
		{"no pin", "192.0.2.10", "naut", "", 50, false, pw, "naut siemens cert --host 192.0.2.10"},
		{"no count", "192.0.2.10", "naut", "ab", 0, false, pw, "--status"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveSiemensDiag(siemensDiagConfig{count: c.count, status: c.status}, c.host, c.user, c.pin, none, c.getenv)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want …%s…", err, c.want)
			}
		})
	}
	// --status needs no count.
	if _, err := resolveSiemensDiag(siemensDiagConfig{status: true}, "192.0.2.10", "naut", "ab", none, pw); err != nil {
		t.Fatal(err)
	}
}

func TestSiemensDiagTakesDefaultsFromTheProject(t *testing.T) {
	dir := t.TempDir()
	m := `name: s7
tasks:
  - program: main.st
    scan: 100ms
target:
  siemens:
    station:
      cpu: {name: PLC_1, order: 6ES7 510-1DK03-0AB0, firmware: V3.1, address: 192.0.2.10, subnet-mask: 255.255.255.0, profinet-name: plc-1}
    online: {user: naut, cert-sha256: "` + strings.Repeat("ab", 32) + `"}
`
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := resolveSiemensDiag(siemensDiagConfig{count: 5}, "", "", "", dir, pw)
	if err != nil || c.host != "192.0.2.10" || c.user != "naut" || c.pin != strings.Repeat("ab", 32) || c.password != "pw" {
		t.Fatalf("%+v %v", c, err)
	}
}

// diagFake is a fake CPU with a two-entry buffer (synthetic texts), a user
// who may read it and one who may not.
func diagFake(t *testing.T, setup ...func(*webapitest.Server)) (*webapitest.Server, func(user string) *webapi.Client) {
	t.Helper()
	s := webapitest.New()
	s.Users["diag"] = webapitest.User{Password: "pw", Permissions: []string{"read_diagnostics"}}
	s.Users["viewer"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	t0 := time.Date(2026, 11, 17, 11, 0, 0, 0, time.UTC)
	s.Diag = []webapitest.DiagEntry{
		{Time: t0.Add(time.Minute), Incoming: true, TextList: 9, Text: 7, Short: "Download\nof blocks", Long: "Blocks loaded\n\nPLC_1 / PLC_1", Help: "nothing to do"},
		{Time: t0, TextList: 2, Text: 16385, Short: "Boot up", Long: "CPU changes from OFF to STOP"},
	}
	for _, f := range setup {
		f(s)
	}
	url, pin := s.Start(t)
	return s, func(user string) *webapi.Client {
		c, err := webapi.New(url, webapi.Options{User: user, Password: "pw", CertSHA256: pin})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
}

func TestSiemensDiagPrintsTheBufferAndLogsOut(t *testing.T) {
	s, client := diagFake(t)
	var out, errb bytes.Buffer
	cfg := siemensDiagConfig{host: "cpu", user: "diag", lang: "en-US", count: 50, long: true}
	if code := siemensDiag(context.Background(), cfg, client("diag"), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := `diagnostic buffer of cpu: 2 of 3200 entries, last changed 2026-11-17T11:01:00.000000000Z
2026-11-17T11:01:00.000000000Z  incoming  9/7  Download of blocks
    Blocks loaded
    PLC_1 / PLC_1
    nothing to do
2026-11-17T11:00:00.000000000Z  outgoing  2/16385  Boot up
    CPU changes from OFF to STOP
`
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
	if n := s.Sessions(); n != 0 {
		t.Fatalf("sessions after diag = %d, want 0 (logged out)", n)
	}
}

func TestSiemensDiagStatusAndJSON(t *testing.T) {
	_, client := diagFake(t)
	var out, errb bytes.Buffer
	cfg := siemensDiagConfig{host: "cpu", user: "diag", status: true, json: true}
	if code := siemensDiag(context.Background(), cfg, client("diag"), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var b webapi.DiagBuffer
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if b.Count != 2 || b.Max != 3200 || len(b.Entries) != 0 || b.LastModified != "2026-11-17T11:01:00.000000000Z" {
		t.Fatalf("status: %+v", b)
	}
}

func TestSiemensDiagRefusalsNameTheFix(t *testing.T) {
	for _, c := range []struct {
		name, user string
		setup      func(*webapitest.Server)
		want       string
	}{
		{"no diagnostics right", "viewer", nil, `the web user "viewer" needs the diagnostics right`},
		{"older firmware", "diag", func(s *webapitest.Server) { s.Methods = []string{"Api.Login", "Api.Logout", "Api.Ping"} }, "firmware 3.1 or later"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var setup []func(*webapitest.Server)
			if c.setup != nil {
				setup = append(setup, c.setup)
			}
			s, client := diagFake(t, setup...)
			var out, errb bytes.Buffer
			cfg := siemensDiagConfig{host: "cpu", user: c.user, lang: "en-US", count: 5}
			if code := siemensDiag(context.Background(), cfg, client(c.user), &out, &errb); code != 1 {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(errb.String(), c.want) || out.Len() != 0 {
				t.Fatalf("stderr %q stdout %q, want …%s…", errb.String(), out.String(), c.want)
			}
			if n := s.Sessions(); n != 0 {
				t.Fatalf("sessions after a refused read = %d, want 0", n)
			}
		})
	}
}
