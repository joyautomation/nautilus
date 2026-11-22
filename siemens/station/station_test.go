package station

import (
	"strings"
	"testing"
)

func TestCheckAgentURL(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8190", "https://tiad.example:8190/"} {
		if err := CheckAgentURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// Every bad input carries "s3cret": no error may contain it, whatever
	// shape the mistake takes.
	for _, bad := range []string{
		"tiad-s3cret", "ftp://u:s3cret@h", "http://", "https://u:s3cret@h", "https://s3cret@h",
		"admin:s3cret@10.0.0.5:8080", "http//u:s3cret@h", "u:s3cret@h", "//u:s3cret@h:8190",
		"http://h:8190/?token=s3cret", "https://u:s3cret@h/?token=s3cret", "http://h/#s3cret", "http://h/?",
		"http://h:s3cret", "http:s3cret@h", "mailto:s3cret@h",
	} {
		err := CheckAgentURL(bad)
		if err == nil {
			t.Errorf("%s: want an error", bad)
		} else if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: the error echoes the credential: %v", bad, err)
		}
	}
}

func TestCheckCertSHA256(t *testing.T) {
	good := strings.Repeat("aB", 32)
	if err := CheckCertSHA256(good); err != nil {
		t.Error(err)
	}
	if err := CheckCertSHA256(strings.Repeat("ab:", 31) + "ab"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "abc", strings.Repeat("zz", 32), strings.Repeat("ab", 33)} {
		if CheckCertSHA256(bad) == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestStationNames(t *testing.T) {
	for _, n := range []string{"plc-1", "a.b-c", "port-1234", "port-x", "1000.1.1.1"} {
		if err := CheckStationName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	for _, n := range []string{"", "PLC", "-a", "a-", "a..b", "300.1.1.1", "01.2.3.4", "port-001", "port-001-00002", "port-001.x", strings.Repeat("a", 64)} {
		if CheckStationName(n) == nil {
			t.Errorf("%q: want an error", n)
		}
	}
}

// TestConfigurationProtectionProblems: empty is the default, the three
// values pass, anything else is a problem at its YAML path.
func TestConfigurationProtectionProblems(t *testing.T) {
	cpu := CPU{Name: "PLC_1", Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1",
		Address: "192.0.2.10", SubnetMask: "255.255.255.0", ProfinetName: "plc-1"}
	for _, ok := range []string{"", "password", "none", "keep"} {
		c := cpu
		c.ConfigurationProtection = ok
		if ps := (Station{CPU: c}).Problems(); len(ps) != 0 {
			t.Errorf("%q: %v", ok, ps)
		}
	}
	c := cpu
	c.ConfigurationProtection = "off"
	ps := (Station{CPU: c}).Problems()
	if len(ps) != 1 || ps[0].String() != `cpu.configuration-protection: "off" is not one of password, none, keep` {
		t.Errorf("off: %v", ps)
	}
}

// TestAccessControlProblems: empty is the default, the manifest's two
// values pass, and anything else, the wire's spelling included, is a
// problem at its YAML path.
func TestAccessControlProblems(t *testing.T) {
	cpu := CPU{Name: "PLC_1", Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1",
		Address: "192.0.2.10", SubnetMask: "255.255.255.0", ProfinetName: "plc-1"}
	for _, ok := range []string{"", "full-access-user", "keep"} {
		c := cpu
		c.AccessControl = ok
		if ps := (Station{CPU: c}).Problems(); len(ps) != 0 {
			t.Errorf("%q: %v", ok, ps)
		}
	}
	for _, bad := range []string{"off", "fullAccessUser", "disabled"} {
		c := cpu
		c.AccessControl = bad
		ps := (Station{CPU: c}).Problems()
		if want := `cpu.access-control: "` + bad + `" is not one of full-access-user, keep`; len(ps) != 1 || ps[0].String() != want {
			t.Errorf("%s: %v, want %s", bad, ps, want)
		}
	}
	if err := CheckAccessControl("full-access-user"); err == nil {
		t.Error("the wire check takes the manifest's spelling")
	}
}

// TestCheckTiaPassword: the rules TIA words, every broken one named, and
// never the password itself.
func TestCheckTiaPassword(t *testing.T) {
	if err := CheckTiaPassword("Bench-config-2026"); err != nil {
		t.Error(err)
	}
	err := CheckTiaPassword("s3cretpass")
	if err == nil || !strings.HasSuffix(err.Error(), ": no upper-case letter (A-Z)") || strings.Contains(err.Error(), "s3cretpass") {
		t.Errorf("s3cretpass: %v", err)
	}
}

// TestWebUserProblems: the web user name rule (shared with the wire and
// tiad), duplicates ignoring case, and users only with the server on.
func TestWebUserProblems(t *testing.T) {
	cpu := CPU{Name: "PLC_1", Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1",
		Address: "192.0.2.10", SubnetMask: "255.255.255.0", ProfinetName: "plc-1"}
	for _, ok := range []string{"op", "op.1_a-b", strings.Repeat("a", 32)} {
		if err := CheckWebUserName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1op", "_op", "op name", "op\n", strings.Repeat("a", 33), "été"} {
		if err := CheckWebUserName(bad); err == nil || !strings.Contains(err.Error(), "a web user name is 1 to 32 characters") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, r := range []string{"nautilus", "NAUTILUS", "Anonymous", "anonymous"} {
		if err := CheckWebUserName(r); err == nil || !strings.Contains(err.Error(), "is reserved") {
			t.Errorf("%q: %v", r, err)
		}
	}
	ws := func(on bool, names ...string) *WebServer {
		w := &WebServer{Enabled: on}
		for _, n := range names {
			w.Users = append(w.Users, WebUser{Name: n, Rights: []string{"read"}})
		}
		return w
	}
	got := func(w *WebServer) string {
		var out []string
		for _, p := range (Station{CPU: cpu, WebServer: w}).Problems() {
			out = append(out, p.String())
		}
		return strings.Join(out, "; ")
	}
	if g := got(ws(true, "op", "OP")); g != `web-server.users[1].name: "OP" twice (names are compared ignoring case)` {
		t.Errorf("case: %s", g)
	}
	if g := got(ws(true, "Anonymous")); !strings.HasPrefix(g, `web-server.users[0].name: "Anonymous" is reserved`) {
		t.Errorf("reserved: %s", g)
	}
	if g := got(ws(false, "op")); g != "web-server.users: is set and web-server.enabled is false: a web server that is off has no users" {
		t.Errorf("off with users: %s", g)
	}
	if g := got(ws(true, "op")); g != "" {
		t.Errorf("fine: %s", g)
	}
}
