package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/internal/project"
	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
)

func nautAuth(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := authMain(args, &out, &errb, os.Getenv)
	return code, out.String(), errb.String()
}

// naut auth init on what naut new writes, every template: the file gains
// a block at its end and nothing else changes (the person's comments and
// layout stay), the project still loads in full, the user given holds the
// role, and the command names the next steps, restart included.
func TestAuthInitOnEveryTemplate(t *testing.T) {
	for _, tmpl := range []string{tmplMinimal, tmplDemo} { // the manifest templates (the Go ones have none)
		t.Run(tmpl, func(t *testing.T) {
			state := isolateState(t)
			dir := scaffoldInto(t, scaffold{Name: "plant", Program: "Plant", Template: tmpl})
			path := filepath.Join(dir, "nautilus.yaml")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			code, _, errs := nautAuth("init", "--project", dir, "--user", "alice:operator", "--user", "Bob:administrator")
			if code != 0 {
				t.Fatalf("auth init: %d %s", code, errs)
			}
			after, _ := os.ReadFile(path)
			if got := removeLines(string(after), string(before)); got != "" {
				t.Errorf("the original manifest is not kept line for line (left: %q):\n%s", got, after)
			}
			// server.tls: self-signed, under the template's server: block,
			// and the certificate naut run here will serve, its fingerprint
			// told.
			if !strings.Contains(string(after), "server:\n  tls: self-signed") {
				t.Errorf("no server.tls: self-signed under server:\n%s", after)
			}
			leaf := leafOf(t, filepath.Join(state, "nautilus", "plant", "tls", "self-signed.pem"))
			if !strings.Contains(errs, certFingerprint(leaf.Raw)) || !strings.Contains(errs, "compare that fingerprint") {
				t.Errorf("auth init does not tell the certificate's fingerprint:\n%s", errs)
			}
			if p, err := project.Load(os.DirFS(dir), ""); err != nil {
				t.Fatalf("the project no longer loads: %v\n%s", err, after)
			} else if !p.TLSSelfSigned() {
				t.Error("the project does not serve self-signed")
			}
			m, err := project.ReadManifest(os.DirFS(dir), "")
			if err != nil {
				t.Fatal(err)
			}
			p := m.AuthPolicy()
			if p == nil || !p.HasUser("alice") || !p.HasUser("bob") {
				t.Fatalf("alice and bob are not in auth.users:\n%s", after)
			}
			if !p.Can(auth.User("alice"), "write", "X").Allowed || p.Can(auth.User("alice"), "users", "").Allowed {
				t.Error("alice is not exactly an operator")
			}
			if !p.Can(auth.User("bob"), "users", "").Allowed {
				t.Error("bob is not an administrator")
			}
			for _, want := range []string{"naut users add alice", "naut users add bob", restartNote, "reads too"} {
				if !strings.Contains(errs, want) {
					t.Errorf("auth init does not say %q:\n%s", want, errs)
				}
			}
			// Twice is refused, and leaves the file alone.
			if code, _, errs := nautAuth("init", "--project", dir, "--user", "carol:viewer"); code != 1 || !strings.Contains(errs, "has an auth: block already") {
				t.Errorf("a second init: %d %s", code, errs)
			}
			if again, _ := os.ReadFile(path); !bytes.Equal(again, after) {
				t.Error("a refused init changed the file")
			}
		})
	}
}

func authProject(t *testing.T, manifest string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nEND_PROGRAM\n"), 0o644)
	return dir
}

const plainManifest = "name: line1\ntasks:\n  - program: main.st\n    scan: 100ms\n"

// What naut auth init refuses, leaving the file as it was: a spec that is
// not <name>:<role>, a part that is not a plain name (nothing typed may
// add a YAML key), a user name naut users would refuse, a role or area the
// block does not define, a user given twice, and a manifest that does
// not load to begin with.
func TestAuthInitRefuses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		args     []string
		want     string
	}{
		{"no role", plainManifest, []string{"--user", "alice"}, "give <name>:<role>"},
		{"empty role", plainManifest, []string{"--user", "alice:"}, "not a plain name"},
		{"injection", plainManifest, []string{"--user", "alice:operator]\n  anonymous: viewer\n  x: [y"}, "not a plain name"},
		{"flow injection", plainManifest, []string{"--user", "alice:operator, administrator"}, "not a plain name"},
		{"bad user name", plainManifest, []string{"--user", "-alice:operator"}, "not a plain name"},
		{"user name naut users refuses", plainManifest, []string{"--user", strings.Repeat("a", 65) + ":operator"}, "1 to 64"},
		{"unknown role", plainManifest, []string{"--user", "alice:boss"}, "would not load"},
		{"unknown area", plainManifest, []string{"--user", "alice:operator@receiving"}, "would not load"},
		{"twice", plainManifest, []string{"--user", "alice:operator", "--user", "ALICE:viewer"}, "given twice"},
		{"broken manifest", plainManifest + "bogus: 1\n", []string{"--user", "alice:operator"}, "fix that first"},
		{"auth already", plainManifest + "auth:\n  users: {}\n", nil, "auth: block already"},
		{"an argument", plainManifest, []string{"alice"}, "takes no arguments"},
		// -m spelt another way than ReadManifest cleans it to must still
		// check the edited text, not the file on disk.
		{"-m ./ unknown role", plainManifest, []string{"-m", "./nautilus.yaml", "--user", "alice:boss"}, "would not load"},
		// Text that loads but says something else: a key YAML reads as
		// null, a block after the document's end.
		{"a name YAML reads as null", plainManifest, []string{"--user", "null:operator"}, "is unchanged"},
		{"after the document end", plainManifest + "...\n", []string{"--user", "alice:operator"}, "exactly what was meant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateState(t)
			dir := authProject(t, tc.manifest)
			code, _, errs := nautAuth(append([]string{"init", "--project", dir}, tc.args...)...)
			if code == 0 || !strings.Contains(errs, tc.want) {
				t.Errorf("%d, want a refusal saying %q:\n%s", code, tc.want, errs)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "nautilus.yaml")); string(got) != tc.manifest {
				t.Errorf("a refused init changed the file:\n%s", got)
			}
		})
	}
}

// With no --user the block names nobody, loads, and says so: until
// auth.users names someone only NAUTILUS_TOKEN and service tokens work.
// The file's own line ending (CRLF) is kept, and a last line with no line
// ending gets one before the block.
func TestAuthInitWithNoUsersAndCRLF(t *testing.T) {
	isolateState(t)
	orig := strings.ReplaceAll(strings.TrimSuffix(plainManifest, "\n"), "\n", "\r\n")
	dir := authProject(t, orig)
	os.Chmod(filepath.Join(dir, "nautilus.yaml"), 0o640)
	code, _, errs := nautAuth("init", "--project", dir, "--no-tls")
	if code != 0 || !strings.Contains(errs, "only NAUTILUS_TOKEN") {
		t.Fatalf("%d %s", code, errs)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "nautilus.yaml"))
	if st, _ := os.Stat(filepath.Join(dir, "nautilus.yaml")); st.Mode().Perm() != 0o640 {
		t.Errorf("the file's mode is not kept: %v", st.Mode())
	}
	if !bytes.HasPrefix(got, []byte(orig+"\r\n\r\n")) || bytes.Contains(bytes.ReplaceAll(got, []byte("\r\n"), nil), []byte("\n")) {
		t.Errorf("CRLF not kept:\n%q", got)
	}
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	if m.AuthPolicy() == nil || m.AuthPolicy().HasUsers() {
		t.Error("want an auth: block with no users")
	}
}

// opcuaTrustProject is a project under auth: with areas, and a PKI with
// two refused client certificates.
func opcuaTrustProject(t *testing.T, authBlock string) (dir string, thumbs []string) {
	t.Helper()
	dir = authProject(t, plainManifest+"tags:\n  - { name: RCV_SP, role: setpoint, type: REAL, init: 1 }\n"+authBlock)
	rej := filepath.Join(dir, "pki", "rejected", "certs")
	os.MkdirAll(rej, 0o700)
	for _, uri := range []string{"urn:test:scada", "urn:test:historian"} {
		der, _ := opcuaClientCert(t, uri)
		tp := opcuaserver.Thumbprint(der)
		os.WriteFile(filepath.Join(rej, tp+".der"), der, 0o644)
		thumbs = append(thumbs, tp)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(cwd) })
	return dir, thumbs
}

func nautTrust(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := opcuaTrustMain(args, &out, &errb)
	return code, out.String(), errb.String()
}

func trustedHas(t *testing.T, dir, tp string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "pki", "trusted", "certs", tp+".der"))
	return err == nil
}

// naut opcua trust <prefix> --as <name>:<role>[@area]: the certificate
// moves from rejected/ to trusted/, and auth.opcua-certificates maps its
// FULL thumbprint (not the prefix typed) to the grant, so the policy names
// the certificate; the lines are inserted and nothing else in the file
// changes; the second mapping goes under the key the first made; the
// output says the grant needs a restart.
func TestOPCUATrustAsMapsTheCertificate(t *testing.T) {
	block := "auth:\n  # the areas\n  areas:\n    receiving: {tags: [RCV_*]}\n\n  users:\n    akhan: [engineer]   # a comment kept\n"
	dir, tps := opcuaTrustProject(t, block)
	path := filepath.Join(dir, "nautilus.yaml")
	before, _ := os.ReadFile(path)
	code, out, errs := nautTrust(tps[0][:16], "--as", "scada:operator@receiving")
	if code != 0 {
		t.Fatalf("trust --as: %d %s %s", code, out, errs)
	}
	if !trustedHas(t, dir, tps[0]) {
		t.Error("the certificate is not in trusted/")
	}
	for _, want := range []string{"trusted " + tps[0], "auth.opcua-certificates.scada", restartNote} {
		if !strings.Contains(out, want) {
			t.Errorf("trust --as does not say %q:\n%s", want, out)
		}
	}
	after, _ := os.ReadFile(path)
	if got := removeLines(string(after), string(before)); got != "" {
		t.Errorf("more than the mapping changed (lines left after removing the inserted ones: %q):\n%s", got, after)
	}
	// The second goes under the key the first made, as a viewer of the
	// whole controller.
	if code, out, errs := nautTrust(tps[1], "--as", "historian:viewer"); code != 0 {
		t.Fatalf("second trust --as: %d %s %s", code, out, errs)
	}
	final, _ := os.ReadFile(path)
	if strings.Count(string(final), "opcua-certificates:") != 1 {
		t.Errorf("a second opcua-certificates: key:\n%s", final)
	}
	if _, err := project.Load(os.DirFS(dir), ""); err != nil {
		t.Fatalf("the project no longer loads: %v\n%s", err, final)
	}
	m, _ := project.ReadManifest(os.DirFS(dir), "")
	p := m.AuthPolicy()
	if n, ok := p.OPCUACertFor(tps[0], ""); !ok || n != "scada" {
		t.Errorf("the full thumbprint is not mapped to scada: %q %v\n%s", n, ok, final)
	}
	if n, ok := p.OPCUACertFor(tps[1], ""); !ok || n != "historian" {
		t.Errorf("historian: %q %v", n, ok)
	}
	if !p.Can(auth.OPCUACert("scada"), "write", "RCV_SP").Allowed || p.Can(auth.OPCUACert("scada"), "write", "OTHER").Allowed {
		t.Error("scada does not write exactly the receiving area")
	}
	if p.Can(auth.OPCUACert("historian"), "write", "RCV_SP").Allowed {
		t.Error("historian writes")
	}
}

// removeLines is a with each line of b removed once, in order: what a
// pure insertion into b leaves is exactly the inserted lines, so a
// mismatch shows as b's lines left over.
func removeLines(a, b string) string {
	al := strings.SplitAfter(a, "\n")
	var left []string
	i := 0
	for _, l := range strings.SplitAfter(b, "\n") {
		for i < len(al) && al[i] != l {
			i++
		}
		if i == len(al) {
			left = append(left, l)
			continue
		}
		i++
	}
	return strings.Join(left, "")
}

// What trust --as refuses BEFORE the certificate moves: no auth: block
// (no command closes a controller on the side), a name or a certificate
// mapped already, a role or area the block does not define, a spec that
// is not plain names, and an auth: written in flow style (it names the
// line to add). The certificate stays in rejected/ and the manifest as it
// was.
func TestOPCUATrustAsRefusesBeforeMoving(t *testing.T) {
	mapped := func(tp string) string {
		return "auth:\n  users: {}\n  opcua-certificates:\n    scada: {thumbprint: " + tp + ", grants: viewer}\n"
	}
	for _, tc := range []struct {
		name  string
		block func(tps []string) string
		as    string
		want  string
		args  []string
	}{
		{"-m ./ unknown role", func([]string) string { return "auth:\n  users: {}\n" }, "scada:boss", "would not load", []string{"-m", "./nautilus.yaml"}},
		{"a name YAML reads as null", func([]string) string { return "auth:\n  users: {}\n" }, "null:operator", "nothing changed", nil},
		{"no auth", func([]string) string { return "" }, "scada:operator", "no auth: block", nil},
		{"name taken", func(tps []string) string { return mapped(strings.Repeat("ab", 20)) }, "SCADA:operator", "already maps scada", nil},
		{"unknown role", func([]string) string { return "auth:\n  users: {}\n" }, "scada:boss", "would not load", nil},
		{"unknown area", func([]string) string { return "auth:\n  users: {}\n" }, "scada:operator@receiving", "would not load", nil},
		{"not plain", func([]string) string { return "auth:\n  users: {}\n" }, "scada:operator}\n  anonymous: viewer", "not a plain name", nil},
		{"flow auth", func([]string) string { return "auth: {users: {}}\n" }, "scada:operator", "add this under it by hand", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, tps := opcuaTrustProject(t, "")
			path := filepath.Join(dir, "nautilus.yaml")
			raw, _ := os.ReadFile(path)
			orig := string(raw) + tc.block(tps)
			os.WriteFile(path, []byte(orig), 0o644)
			code, out, errs := nautTrust(append([]string{tps[0], "--as", tc.as}, tc.args...)...)
			if code == 0 || !strings.Contains(errs, tc.want) {
				t.Errorf("%d, want a refusal saying %q:\n%s%s", code, tc.want, out, errs)
			}
			if trustedHas(t, dir, tps[0]) {
				t.Error("a refused --as moved the certificate to trusted/")
			}
			if got, _ := os.ReadFile(path); string(got) != orig {
				t.Errorf("a refused --as changed the manifest:\n%s", got)
			}
		})
	}
	// The same certificate twice, under another name: refused, naming the
	// mapping it has.
	dir, tps := opcuaTrustProject(t, mapped("")[:len("auth:\n  users: {}\n")])
	if code, _, errs := nautTrust(tps[0], "--as", "scada:operator"); code != 0 {
		t.Fatal(errs)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "nautilus.yaml"))
	if code, _, errs := nautTrust(tps[0], "--as", "again:operator"); code != 1 || !strings.Contains(errs, "maps this certificate already") {
		t.Errorf("a certificate mapped twice: %d %s", code, errs)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "nautilus.yaml")); !bytes.Equal(before, after) {
		t.Error("the refusal changed the manifest")
	}
}

// A certificate trusted already (plain naut opcua trust earlier) is mapped
// by --as without moving anything, and plain trust under auth: says how to
// let it write.
func TestOPCUATrustAsAnAlreadyTrustedCertificate(t *testing.T) {
	dir, tps := opcuaTrustProject(t, "auth:\n  users: {}\n")
	code, out, _ := nautTrust(tps[0])
	if code != 0 || !strings.Contains(out, "read-only until mapped") || !strings.Contains(out, "--as <name>:<role>") {
		t.Errorf("plain trust under auth: %d\n%s", code, out)
	}
	code, out, errs := nautTrust(tps[0], "--as", "scada:operator")
	if code != 0 || !strings.Contains(out, "is trusted already") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	m, _ := project.ReadManifest(os.DirFS(dir), "")
	if n, ok := m.AuthPolicy().OPCUACertFor(tps[0], ""); !ok || n != "scada" {
		t.Error("not mapped")
	}
	// Without auth:, plain trust says nothing of mapping.
	dir2, tps2 := opcuaTrustProject(t, "")
	_ = dir2
	if _, out, _ := nautTrust(tps2[0]); strings.Contains(out, "read-only") {
		t.Errorf("plain trust without auth: talks of mapping:\n%s", out)
	}
}

// On a box naut deploy set up there is no nautilus.yaml beside the store:
// --as refuses, moves nothing, and prints the lines to add in the
// checkout with the full thumbprint.
func TestOPCUATrustAsWithNoManifestHere(t *testing.T) {
	dir, tps := opcuaTrustProject(t, "")
	os.Remove(filepath.Join(dir, "nautilus.yaml"))
	code, _, errs := nautTrust(tps[0][:20], "--as", "scada:operator", "--pki", filepath.Join(dir, "pki"))
	if code != 1 || !strings.Contains(errs, "scada: {thumbprint: "+tps[0]+", grants: [operator]}") || !strings.Contains(errs, "nothing changed") {
		t.Errorf("%d %s", code, errs)
	}
	if trustedHas(t, dir, tps[0]) {
		t.Error("moved the certificate")
	}
}

// -m names the manifest for both halves: its opcua.pki is the store the
// certificate moves in, and the mapping is written into it, not into
// nautilus.yaml.
func TestOPCUATrustAsWithAnotherManifest(t *testing.T) {
	dir, tps := opcuaTrustProject(t, "auth:\n  users: {}\n")
	plain, _ := os.ReadFile(filepath.Join(dir, "nautilus.yaml"))
	os.Rename(filepath.Join(dir, "pki"), filepath.Join(dir, "sitepki"))
	site := string(plain) + "opcua:\n  pki: sitepki\n"
	os.MkdirAll(filepath.Join(dir, "sites"), 0o755)
	os.WriteFile(filepath.Join(dir, "sites", "a.yaml"), []byte(site), 0o644)
	if code, out, errs := nautTrust(tps[0], "--as", "scada:operator", "-m", "sites/a.yaml"); code != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "sitepki", "trusted", "certs", tps[0]+".der")); err != nil {
		t.Error("the certificate did not move in the store sites/a.yaml names")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "nautilus.yaml")); !bytes.Equal(got, plain) {
		t.Error("nautilus.yaml changed")
	}
	m, err := project.ReadManifest(os.DirFS(dir), "sites/a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := m.AuthPolicy().OPCUACertFor(tps[0], ""); !ok || n != "scada" {
		t.Error("sites/a.yaml does not map it")
	}
}

// YAML reads a lone CR, U+0085, U+2028 and U+2029 as line breaks, so its
// line numbers and the file's disagree after one: both commands refuse a
// manifest holding one, naming it and its line, and change nothing (Fable
// on #1112). With that refusal off, the exact check after the edit
// refuses the misplaced edit by itself.
func TestManifestEditRefusesOddLineBreaks(t *testing.T) {
	block := "auth:\n  users:\n    akhan: [engineer]\n"
	for _, odd := range []struct{ text, name string }{
		{"\r", "carriage return"}, {"\u0085", "U+0085"}, {" ", "U+2028"}, {" ", "U+2029"},
	} {
		for _, guard := range []bool{true, false} {
			t.Run(fmt.Sprintf("%q guard %v", odd.text, guard), func(t *testing.T) {
				isolateState(t)
				if !guard {
					old := refuseOddBreaks
					refuseOddBreaks = func([]byte) error { return nil }
					t.Cleanup(func() { refuseOddBreaks = old })
				}
				dir, tps := opcuaTrustProject(t, "# one"+odd.text+"two\n"+block)
				path := filepath.Join(dir, "nautilus.yaml")
				orig, _ := os.ReadFile(path)
				code, out, errs := nautTrust(tps[0], "--as", "scada:operator")
				if code == 0 {
					t.Fatalf("trust --as was accepted:\n%s%s", out, errs)
				}
				if guard && (!strings.Contains(errs, odd.name) || !strings.Contains(errs, "line 7")) {
					t.Errorf("the refusal does not name %s on line 7:\n%s%s", odd.name, out, errs)
				}
				if trustedHas(t, dir, tps[0]) {
					t.Error("the certificate moved")
				}
				if got, _ := os.ReadFile(path); !bytes.Equal(got, orig) {
					t.Errorf("the manifest changed:\n%q", got)
				}
				// auth init, on a manifest with no auth: yet.
				dir2 := authProject(t, plainManifest+"# one"+odd.text+"two\n")
				if code, _, errs := nautAuth("init", "--project", dir2, "--user", "alice:operator"); guard && (code == 0 || !strings.Contains(errs, odd.name)) {
					t.Errorf("auth init: %d %s", code, errs)
				}
			})
		}
	}
}

// A symlinked nautilus.yaml stays a symlink: the file it points to is
// edited (Fable on #1112).
func TestManifestEditFollowsASymlink(t *testing.T) {
	isolateState(t)
	dir := authProject(t, plainManifest)
	real := filepath.Join(t.TempDir(), "plant.yaml")
	os.Rename(filepath.Join(dir, "nautilus.yaml"), real)
	if err := os.Symlink(real, filepath.Join(dir, "nautilus.yaml")); err != nil {
		t.Skip(err)
	}
	if code, _, errs := nautAuth("init", "--project", dir, "--user", "alice:operator"); code != 0 {
		t.Fatal(errs)
	}
	if st, err := os.Lstat(filepath.Join(dir, "nautilus.yaml")); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Error("nautilus.yaml is no longer a symlink")
	}
	if got, _ := os.ReadFile(real); !strings.Contains(string(got), "alice: [operator]") {
		t.Errorf("the target was not edited:\n%s", got)
	}
}

// A prefix is looked up across rejected/ and trusted/ together: one that
// names a certificate in each is ambiguous (not a pick of the rejected
// one); the same certificate in both is the trusted one (Fable on #1112).
func TestPickClientCert(t *testing.T) {
	ci := func(tp string) opcuaserver.CertInfo { return opcuaserver.CertInfo{Thumbprint: tp} }
	a := "0123456789abcdef" + strings.Repeat("a", 24)
	b := "0123456789abcdef" + strings.Repeat("b", 24)
	if _, _, err := pickClientCert("0123456789abcdef", []opcuaserver.CertInfo{ci(a)}, []opcuaserver.CertInfo{ci(b)}); err == nil || !strings.Contains(err.Error(), "2 certificates") {
		t.Errorf("one trusted, one rejected under one prefix: %v", err)
	}
	if c, rej, err := pickClientCert(a[:20], []opcuaserver.CertInfo{ci(a)}, []opcuaserver.CertInfo{ci(b)}); err != nil || rej || c.Thumbprint != a {
		t.Errorf("the trusted one: %v %v %v", c, rej, err)
	}
	if c, rej, err := pickClientCert(b[:20], []opcuaserver.CertInfo{ci(a)}, []opcuaserver.CertInfo{ci(b)}); err != nil || !rej || c.Thumbprint != b {
		t.Errorf("the rejected one: %v %v %v", c, rej, err)
	}
	if _, rej, err := pickClientCert(a, []opcuaserver.CertInfo{ci(a)}, []opcuaserver.CertInfo{ci(a)}); err != nil || rej {
		t.Errorf("the same certificate in both: %v %v", rej, err)
	}
	if _, _, err := pickClientCert("ffff", nil, []opcuaserver.CertInfo{ci(a)}); err == nil {
		t.Error("no match")
	}
}

// A certificate the block maps by its subject, mapped again by its
// thumbprint: the command says the new name supersedes the old.
func TestOPCUATrustAsSupersedesASubjectMapping(t *testing.T) {
	dir, tps := opcuaTrustProject(t, "")
	_, _, rejected, err := opcuaserver.PKI{Dir: filepath.Join(dir, "pki")}.Certs()
	if err != nil || len(rejected) == 0 {
		t.Fatal(err)
	}
	subj := rejected[0].Subject
	f, _ := os.OpenFile(filepath.Join(dir, "nautilus.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("auth:\n  users: {}\n  opcua-certificates:\n    bysubject: {subject: \"" + subj + "\", grants: viewer}\n")
	f.Close()
	code, out, errs := nautTrust(rejected[0].Thumbprint, "--as", "scada:operator")
	if code != 0 || !strings.Contains(out, "bysubject maps this certificate by its subject") || !strings.Contains(out, "supersedes") {
		t.Errorf("%d\n%s%s", code, out, errs)
	}
	_ = tps
}

// The manifest is written before the certificate moves: a mapping for a
// certificate not trusted yet does nothing, a trusted certificate the
// manifest does not map yet is open. When the write fails (here, a
// project directory nobody may write to), the certificate stays refused.
func TestOPCUATrustAsWritesTheManifestFirst(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs a directory the test cannot write to")
	}
	dir, tps := opcuaTrustProject(t, "auth:\n  users: {}\n")
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	code, out, errs := nautTrust(tps[0], "--as", "scada:operator")
	if code == 0 || !strings.Contains(errs, "nothing changed") {
		t.Errorf("%d %s%s", code, out, errs)
	}
	if trustedHas(t, dir, tps[0]) {
		t.Error("the certificate was trusted although its mapping was not written")
	}
}

// The exact checks after an edit: a result with anything else changed is
// refused, though it holds what was meant.
func TestExactEditChecks(t *testing.T) {
	read := func(text string) *project.Manifest {
		t.Helper()
		m, err := project.ReadManifest(fstestMap(text), "")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	tp := strings.Repeat("ab", 20)
	want := project.AuthCert{Thumbprint: tp, Grants: project.AuthGrants{{Role: "operator"}}}
	entry := "  opcua-certificates:\n    scada: {thumbprint: " + tp + ", grants: [operator]}\n"
	pre := read(plainManifest + "auth:\n  users:\n    akhan: [engineer]\n")
	if !exactCertEdit(pre, read(plainManifest+"auth:\n"+entry+"  users:\n    akhan: [engineer]\n"), "scada", want) {
		t.Error("the exact edit is refused")
	}
	for name, post := range map[string]string{
		"another auth key":   plainManifest + "auth:\n" + entry + "  anonymous: viewer\n  users:\n    akhan: [engineer]\n",
		"a user changed":     plainManifest + "auth:\n" + entry + "  users:\n    akhan: [viewer]\n",
		"outside auth:":      strings.Replace(plainManifest, "100ms", "200ms", 1) + "auth:\n" + entry + "  users:\n    akhan: [engineer]\n",
		"other grants":       plainManifest + "auth:\n  opcua-certificates:\n    scada: {thumbprint: " + tp + ", grants: [administrator]}\n  users:\n    akhan: [engineer]\n",
		"a second new entry": plainManifest + "auth:\n" + entry + "    more: {subject: CN=x, grants: viewer}\n  users:\n    akhan: [engineer]\n",
	} {
		if exactCertEdit(pre, read(post), "scada", want) {
			t.Errorf("trust --as: %s is taken as exact", name)
		}
	}
	initWant := &project.AuthConfig{Users: map[string][]project.AuthGrant{"alice": {{Role: "operator"}}}}
	plain := read(plainManifest)
	if !exactInit(plain, read(plainManifest+"auth:\n  users:\n    alice: [operator]\n"), initWant, false) {
		t.Error("the exact init is refused")
	}
	for name, post := range map[string]string{
		"another auth key": plainManifest + "auth:\n  anonymous: viewer\n  users:\n    alice: [operator]\n",
		"outside auth:":    strings.Replace(plainManifest, "100ms", "200ms", 1) + "auth:\n  users:\n    alice: [operator]\n",
		"another user":     plainManifest + "auth:\n  users:\n    alice: [operator]\n    bob: [viewer]\n",
	} {
		if exactInit(plain, read(post), initWant, false) {
			t.Errorf("init: %s is taken as exact", name)
		}
	}
	// With server.tls: self-signed written (DX-B): exact only with it, and
	// only it, in server:.
	withTLS := plainManifest + "server:\n  tls: self-signed\nauth:\n  users:\n    alice: [operator]\n"
	if !exactInit(plain, read(withTLS), initWant, true) {
		t.Error("the exact init with server.tls is refused")
	}
	if exactInit(plain, read(withTLS), initWant, false) {
		t.Error("a server.tls not meant is taken as exact")
	}
	if exactInit(plain, read(plainManifest+"auth:\n  users:\n    alice: [operator]\n"), initWant, true) {
		t.Error("init meant to write server.tls, and the result has none: taken as exact")
	}
	if exactInit(plain, read(plainManifest+"server:\n  tls: self-signed\n  addr: x:1\nauth:\n  users:\n    alice: [operator]\n"), initWant, true) {
		t.Error("server: changed beside tls: taken as exact")
	}
}

func fstestMap(manifest string) fstest.MapFS {
	return fstest.MapFS{"nautilus.yaml": {Data: []byte(manifest)}, "main.st": {Data: []byte("PROGRAM Main\nEND_PROGRAM\n")}}
}

// A mixed-case --as name is taken as its lower case, as user names are:
// the mapping is written and checked under that name, so the exact check
// (which compares the policy's lower-cased name) passes, and the
// certificate writes as opcua-cert:scada (naut-lead on #1112).
func TestOPCUATrustAsMixedCaseName(t *testing.T) {
	dir, tps := opcuaTrustProject(t, "auth:\n  users: {}\n")
	code, out, errs := nautTrust(tps[0], "--as", "SCADA-Main:operator")
	if code != 0 || !strings.Contains(out, "auth.opcua-certificates.scada-main") {
		t.Fatalf("%d\n%s%s", code, out, errs)
	}
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := m.AuthPolicy().OPCUACertFor(tps[0], ""); !ok || n != "scada-main" {
		t.Errorf("mapped as %q %v", n, ok)
	}
	if !trustedHas(t, dir, tps[0]) {
		t.Error("not trusted")
	}
}

// auth init and server.tls (naut-lead: passwords must not cross plain
// HTTP, and auth init must never write a config that then refuses): a
// manifest with no server: gets one; --no-tls, a server.tls of its own, a
// TLS proxy in front and a redundant pair get none, each saying why, and
// the pair's names what to do instead. Every result loads.
func TestAuthInitServerTLS(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		args        []string
		self        bool
		want        string
	}{
		{"no server:", "", nil, true, "server.tls: self-signed: the controller serves HTTPS"},
		{"--no-tls", "server:\n  addr: localhost:8080\n", []string{"--no-tls"}, false, "No server.tls written: --no-tls"},
		{"its own", "server:\n  tls: {cert: /etc/c.pem, key: /etc/k.pem}\n", nil, false, "set already, and kept"},
		{"a TLS proxy", "server:\n  trusted-proxies: [127.0.0.1]\n", nil, false, "names a TLS proxy"},
		{"a pair", "redundancy: {}\n", nil, false, "copy both to both controllers"},
		{"flow server:", "server: {addr: \"localhost:8080\"}\n", nil, false, "Add tls: self-signed under it by hand"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateState(t)
			dir := authProject(t, plainManifest+tc.extra)
			code, _, errs := nautAuth(append([]string{"init", "--project", dir, "--user", "alice:operator"}, tc.args...)...)
			if code != 0 || !strings.Contains(errs, tc.want) {
				t.Fatalf("%d, want %q:\n%s", code, tc.want, errs)
			}
			m, err := project.ReadManifest(os.DirFS(dir), "")
			if err != nil {
				t.Fatalf("the result does not load: %v", err)
			}
			if got := m.Server.TLS != nil && m.Server.TLS.SelfSigned; got != tc.self {
				t.Errorf("self-signed %v, want %v", got, tc.self)
			}
			if !m.AuthPolicy().HasUser("alice") {
				t.Error("no alice")
			}
		})
	}
}

// naut auth init never renews the certificate (only naut run does), and
// when it cannot show a fingerprint it says why (#1114 review).
func TestAuthInitCertificateCases(t *testing.T) {
	state := isolateState(t)
	sd := filepath.Join(state, "nautilus", "line1")
	os.MkdirAll(sd, 0o700)
	old := nearExpiry(t, sd, nil)
	dir := authProject(t, plainManifest)
	code, _, errs := nautAuth("init", "--project", dir, "--user", "alice:operator")
	if code != 0 || !strings.Contains(errs, old.Fingerprint) {
		t.Fatalf("%d:\n%s", code, errs)
	}
	if certFingerprint(leafOf(t, old.Path).Raw) != old.Fingerprint {
		t.Error("auth init renewed the certificate")
	}
	os.WriteFile(old.Path, []byte("not a certificate"), 0o600)
	dir = authProject(t, plainManifest)
	code, _, errs = nautAuth("init", "--project", dir, "--user", "alice:operator")
	if code != 0 || !strings.Contains(errs, "No certificate made here yet, so no fingerprint") || !strings.Contains(errs, "delete it") {
		t.Errorf("%d:\n%s", code, errs)
	}
}

// naut auth init --oidc writes the auth.oidc block (issuer and client id,
// the grants as commented examples), which loads, and names the steps at
// the provider; a value that could add a key is refused; and on a pair it
// is refused, since the load would refuse the block.
func TestAuthInitOIDC(t *testing.T) {
	isolateState(t)
	dir := authProject(t, plainManifest)
	code, _, errs := nautAuth("init", "--project", dir, "--no-tls", "--oidc", "https://login.microsoftonline.com/tenant-1/v2.0", "--client-id", "0a1b-2c3d")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	for _, want := range []string{"redirect URI https://<the controller's host>/auth/oidc/callback", "NAUTILUS_OIDC_SECRET", "auth.oidc.groups"} {
		if !strings.Contains(errs, want) {
			t.Errorf("output lacks %q:\n%s", want, errs)
		}
	}
	if strings.Contains(errs, "only NAUTILUS_TOKEN") {
		t.Errorf("with OIDC it says only the token works:\n%s", errs)
	}
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	o := m.Auth.OIDC
	if o == nil || o.Issuer != "https://login.microsoftonline.com/tenant-1/v2.0" || o.ClientID != "0a1b-2c3d" || o.Groups != nil || !m.AuthPolicy().OIDC() {
		t.Errorf("auth.oidc %+v", o)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "nautilus.yaml"))
	if !strings.Contains(string(got), "#   \"<group id>\": operator") {
		t.Errorf("no commented group example:\n%s", got)
	}

	for _, tc := range []struct {
		manifest string
		args     []string
		want     string
	}{
		{plainManifest, []string{"--oidc", "https://x.example"}, "go together"},
		{plainManifest, []string{"--oidc", "https://x.example\"\n  anonymous: viewer\n#", "--client-id", "a"}, "letters, digits"},
		{plainManifest, []string{"--oidc", "https://x.example", "--client-id", "a b"}, "letters, digits"},
		{plainManifest, []string{"--oidc", "http://x.example", "--client-id", "a"}, "would not load"},
	} {
		dir := authProject(t, tc.manifest)
		code, _, errs := nautAuth(append([]string{"init", "--project", dir, "--no-tls"}, tc.args...)...)
		if code == 0 || !strings.Contains(errs, tc.want) {
			t.Errorf("%v: %d, want %q:\n%s", tc.args, code, tc.want, errs)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "nautilus.yaml")); string(got) != tc.manifest {
			t.Errorf("%v: a refused init changed the file", tc.args)
		}
	}
}
