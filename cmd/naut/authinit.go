package main

// naut auth init (#708): the one step that turns auth on. It writes a
// starter auth: block into nautilus.yaml, the users given and every other
// key as a commented example, so a person secures a controller without
// learning the block's shape first, and edits it from there.

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/project"
)

const authUsage = `naut auth — turn on users, roles and sign-in (#708)

Usage:
  naut auth init [--user <name>:<role>]... [--no-tls] [--project <dir>] [-m <manifest>]
        Write a starter auth: block into nautilus.yaml: the users given,
        and the other keys as commented examples. Refused when the
        manifest has an auth: block already. Unless --no-tls, it also
        writes server.tls: self-signed (HTTPS with a certificate the
        controller makes), and prints that certificate's fingerprint;
        not on a redundant pair, behind a TLS proxy, or with a server.tls.

Roles: viewer, operator, supervisor, engineer, administrator (each adds to
the one before), or a custom one of the block's roles:.

Then: naut users add <name> on the controller (the password lives there,
never in the project), and restart naut run. Guide: Users, roles & sign-in.
`

func runAuth(args []string) int {
	return authMain(args, os.Stdout, os.Stderr, os.Getenv)
}

func authMain(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || args[0] != "init" {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			fmt.Fprint(stdout, authUsage)
			return 0
		}
		fmt.Fprint(stderr, authUsage)
		return 2
	}
	fs := flag.NewFlagSet("naut auth init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, authUsage) }
	dir := fs.String("project", ".", "project directory")
	manifest := fs.String("m", "", manifestFlagUsage)
	noTLS := fs.Bool("no-tls", false, "write no server.tls: self-signed (a TLS proxy in front, or a certificate of your own)")
	var specs []string
	fs.Func("user", "a user and the role to grant, <name>:<role> (repeatable)", func(s string) error {
		specs = append(specs, s)
		return nil
	})
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(pos) != 0 {
		fmt.Fprintf(stderr, "naut auth init takes no arguments (users go in --user <name>:<role>)\n\n%s", authUsage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "naut auth init:", err)
		return 1
	}
	var grants []grantSpec
	seen := map[string]bool{}
	for _, s := range specs {
		g, err := parseGrantSpec(s)
		if err != nil {
			return fail(fmt.Errorf("--user %w", err))
		}
		if err := users.ValidName(g.name); err != nil {
			return fail(fmt.Errorf("--user %s: %w", s, err))
		}
		if seen[g.name] {
			return fail(fmt.Errorf("--user %s is given twice", g.name))
		}
		seen[g.name] = true
		grants = append(grants, g)
	}
	e, err := openManifestEdit(*dir, *manifest)
	if err != nil {
		return fail(err)
	}
	if k, _ := child(e.root, "auth"); k != nil {
		return fail(fmt.Errorf("%s has an auth: block already (line %d): edit it there", e.path, k.Line))
	}
	tlsDone, tlsWhy := addSelfSignedTLS(e, *noTLS)
	e.appendBlock(starterAuth(grants))
	// Exact: the result is the manifest as it was plus an auth: block
	// that names these users with these grants and nothing else.
	want := &project.AuthConfig{Users: map[string][]project.AuthGrant{}}
	for _, g := range grants {
		want.Users[g.name] = []project.AuthGrant{{Role: g.role, Areas: g.areas}}
	}
	if _, err := e.save(func(m *project.Manifest) bool { return exactInit(e.m, m, want, tlsDone) }); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stderr, "%s: wrote a starter auth: block", e.path)
	if len(grants) == 0 {
		fmt.Fprintln(stderr, " with no users: only NAUTILUS_TOKEN (and service tokens) work until auth.users names one")
	} else {
		fmt.Fprintln(stderr)
	}
	fmt.Fprintln(stderr, "With it, every request needs a credential: reads too, unless you set anonymous: viewer.")
	if tlsDone {
		fmt.Fprintln(stderr, "server.tls: self-signed: the controller serves HTTPS with a certificate it makes in its state directory, so passwords never cross the network in the clear.")
		// The certificate this checkout's naut run will serve, made now so
		// its fingerprint can be checked against the browser's.
		if sd, err := controllerStateDir("the self-signed certificate", e.m.AuthStateDir(), e.m.Name, e.dir, true, getenv); err == nil {
			if c, err := ensureSelfSigned(sd, cmp.Or(e.m.Name, "nautilus"), selfSignedHosts(e.m.Server.Addr), time.Now()); err == nil {
				fmt.Fprintf(stderr, "  naut run here serves %s, SHA-256 fingerprint:\n    %s\n", c.Path, c.Fingerprint)
				fmt.Fprintln(stderr, "  A browser warns until you accept it: compare that fingerprint with the one it shows (the warning page, then the certificate's details).")
				fmt.Fprintln(stderr, "  A box makes its own at its first start, and naut run there prints its fingerprint.")
			}
		}
	} else if tlsWhy != "" {
		fmt.Fprintln(stderr, "No server.tls written: "+tlsWhy)
	}
	fmt.Fprintln(stderr, "Next:")
	for _, g := range grants {
		fmt.Fprintf(stderr, "  naut users add %s       # %s's password, kept on the controller (sudo on a box)\n", g.name, g.name)
	}
	fmt.Fprintln(stderr, "  "+restartNote)
	return 0
}

// grantSpec is <name>:<role>[@<area>[,<area>]...], as naut auth init
// --user and naut opcua trust --as take it.
type grantSpec struct {
	name, role string
	areas      []string
}

// parseGrantSpec reads a grantSpec. Every part must be a plain name
// (letters, digits, '-', '_', '.'), so what is written into the manifest
// is always one plain YAML scalar: nothing typed here can add a key.
func parseGrantSpec(s string) (grantSpec, error) {
	name, rest, ok := strings.Cut(s, ":")
	if !ok {
		return grantSpec{}, fmt.Errorf("%q: give <name>:<role>, e.g. alice:operator", s)
	}
	role, areas, hasAreas := strings.Cut(rest, "@")
	g := grantSpec{name: strings.ToLower(name), role: role}
	if hasAreas {
		g.areas = strings.Split(areas, ",")
	}
	for _, p := range append([]string{g.name, g.role}, g.areas...) {
		if !plainName(p) {
			return grantSpec{}, fmt.Errorf("%q: %q is not a plain name (letters, digits, '-', '_' and '.')", s, p)
		}
	}
	return g, nil
}

func plainName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			i > 0 && (r == '-' || r == '_' || r == '.')
		if !ok {
			return false
		}
	}
	return true
}

// yaml is the grant as one flow sequence: [operator], or
// [{role: operator, areas: [receiving]}].
func (g grantSpec) yaml() string {
	if len(g.areas) == 0 {
		return "[" + g.role + "]"
	}
	return "[{role: " + g.role + ", areas: [" + strings.Join(g.areas, ", ") + "]}]"
}

// starterAuth is the block naut auth init writes.
func starterAuth(grants []grantSpec) string {
	var b strings.Builder
	b.WriteString(`# Who may do what (#708; guide: Users, roles & sign-in). The roles are
# here, reviewed in git; passwords and tokens never are: they live on the
# controller (naut users). naut check refuses a typo in this block.
auth:
`)
	if len(grants) == 0 {
		b.WriteString("  users: {}                  # e.g. alice: [operator], then naut users add alice\n")
	} else {
		b.WriteString("  users:                     # viewer < operator < supervisor < engineer < administrator\n")
		for _, g := range grants {
			fmt.Fprintf(&b, "    %s: %s\n", g.name, g.yaml())
		}
	}
	b.WriteString(`  # anonymous: viewer          # a request with no credential may read (a wall screen)
  # session: 12h               # a browser sign-in lasts this long (1m to 168h)
  # elevation: 15m             # its password counts this long for force, edit, deploy... (1m to 12h)
  # areas:                     # tag patterns that scope a grant's writes
  #   receiving: { tags: [RCV_*] }
  # roles:                     # a custom role
  #   lead: { inherits: operator, allow: [shelve] }
  # service-tokens:
  #   historian: viewer        # its secret comes from NAUTILUS_TOKEN_HISTORIAN
  # opcua-certificates:        # naut opcua trust <thumbprint> --as <name>:<role> writes these
  # sparkplug: operator        # what a Sparkplug command may write; without it none writes
`)
	return b.String()
}

// exactInit reports whether post is pre (which has no auth: block) with
// the auth: block want, its users granted in the compiled policy, with
// server.tls: self-signed when tls (and only then), and every other part
// of the manifest the same.
func exactInit(pre, post *project.Manifest, want *project.AuthConfig, tls bool) bool {
	p := post.AuthPolicy()
	if pre.Auth != nil || p == nil || !reflect.DeepEqual(post.Auth, want) {
		return false
	}
	if tls && (pre.Server.TLS != nil || post.Server.TLS == nil || !post.Server.TLS.SelfSigned || post.Server.TLS.Cert != "" || post.Server.TLS.Key != "") {
		return false
	}
	clear := func(m *project.Manifest) { m.Auth = nil }
	if tls {
		clear = func(m *project.Manifest) { m.Auth, m.Server.TLS = nil, nil }
	}
	for name := range want.Users {
		if !p.HasUser(name) {
			return false
		}
	}
	return sameBut(pre, post, clear)
}

// addSelfSignedTLS adds server.tls: self-signed to the manifest, under its
// server: block (or a new one), unless noTLS or the manifest is one where
// that is not the right answer: a server.tls of its own, a redundant pair
// (the load refuses self-signed there: each controller would make its
// own), a TLS proxy in front (server.trusted-proxies), or a server: a
// command cannot add a line to. why says what was not done and what to do.
func addSelfSignedTLS(e *manifestEdit, noTLS bool) (done bool, why string) {
	m := e.m
	switch {
	case noTLS:
		return false, "--no-tls. A password then signs in only from the controller's own loopback, or through a TLS proxy named in server.trusted-proxies."
	case m.Server.TLS != nil:
		return false, "server.tls is set already, and kept."
	case m.Redundancy != nil:
		return false, "a redundant pair serves ONE certificate, and with self-signed each controller would make its own (the load refuses it). Make one certificate and key, copy both to both controllers, and name them: server.tls: {cert: /abs/cert.pem, key: /abs/key.pem}."
	case len(m.Server.TrustedProxies) > 0:
		return false, "server.trusted-proxies names a TLS proxy in front, which serves HTTPS. To serve it from the controller too, add server.tls: self-signed."
	}
	line := "tls: self-signed          # HTTPS with a certificate the controller makes (naut auth init); {cert, key} for your own"
	k, v := child(e.root, "server")
	if k == nil {
		e.appendBlock("server:\n  " + line + "\n")
		return true, ""
	}
	if err := e.insertUnder(k, v, []string{line}); err != nil {
		return false, "server: is not a block a command can add a line to. Add tls: self-signed under it by hand."
	}
	return true, ""
}
