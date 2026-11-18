package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/facade"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var pw = env(map[string]string{"NAUTILUS_SIEMENS_PASSWORD": "pw"})

func TestSiemensServeRefusesWhatWouldBeUnsafe(t *testing.T) {
	none := t.TempDir()
	for _, c := range []struct {
		name, host, user, pin string
		getenv                func(string) string
		want                  string
	}{
		{"no host", "", "naut", "ab", pw, "--host is required"},
		{"no user", "192.0.2.10", "", "ab", pw, "--user is required"},
		{"no password", "192.0.2.10", "naut", "ab", env(nil), "NAUTILUS_SIEMENS_PASSWORD"},
		{"no pin", "192.0.2.10", "naut", "", pw, "naut siemens cert --host 192.0.2.10"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveSiemensServe(c.host, c.user, c.pin, none, nil, time.Second, ":0", c.getenv)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want …%s…", err, c.want)
			}
		})
	}
}

func TestSiemensServeTakesDefaultsFromTheProject(t *testing.T) {
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
	c, err := resolveSiemensServe("", "", "", dir, nil, time.Second, ":0", pw)
	if err != nil {
		t.Fatal(err)
	}
	if c.host != "192.0.2.10" || c.user != "naut" || c.pin != strings.Repeat("ab", 32) || c.password != "pw" {
		t.Fatalf("%+v", c)
	}
	if len(c.roots) != 1 || c.roots[0] != (facade.Root{DB: "Tags"}) {
		t.Fatalf("default roots: %+v", c.roots)
	}
	// Flags win over the project.
	c, err = resolveSiemensServe("192.0.2.99", "other", "cd", dir, []facade.Root{{DB: "Main_DB", Prefix: "Main"}}, time.Second, ":0", pw)
	if err != nil || c.host != "192.0.2.99" || c.user != "other" || c.pin != "cd" || c.roots[0].Prefix != "Main" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestParseRoot(t *testing.T) {
	for in, want := range map[string]facade.Root{
		"Tags":             {DB: "Tags"},
		"Main_DB=Main":     {DB: "Main_DB", Prefix: "Main"},
		" Main_DB = Main ": {DB: "Main_DB", Prefix: "Main"},
	} {
		if got, err := parseRoot(in); err != nil || got != want {
			t.Errorf("parseRoot(%q) = %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "=Main", `"Tags"`, "Main_DB.x=Main", "Main_DB=Ma in"} {
		if _, err := parseRoot(bad); err == nil {
			t.Errorf("parseRoot(%q): want an error", bad)
		}
	}
}
