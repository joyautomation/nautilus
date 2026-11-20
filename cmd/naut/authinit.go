package main

// naut auth init (#708): the one step that turns auth on. It writes a
// starter auth: block into nautilus.yaml, the users given and every other
// key as a commented example, so a person secures a controller without
// learning the block's shape first, and edits it from there.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joyautomation/nautilus/auth/users"
)

const authUsage = `naut auth — turn on users, roles and sign-in (#708)

Usage:
  naut auth init [--user <name>:<role>]... [--project <dir>] [-m <manifest>]
        Write a starter auth: block into nautilus.yaml: the users given,
        and the other keys as commented examples. Refused when the
        manifest has an auth: block already.

Roles: viewer, operator, supervisor, engineer, administrator (each adds to
the one before), or a custom one of the block's roles:.

Then: naut users add <name> on the controller (the password lives there,
never in the project), and restart naut run. Guide: Users, roles & sign-in.
`

func runAuth(args []string) int {
	return authMain(args, os.Stdout, os.Stderr)
}

func authMain(args []string, stdout, stderr io.Writer) int {
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
	e.appendBlock(starterAuth(grants))
	if _, err := e.save(); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stderr, "%s: wrote a starter auth: block", e.path)
	if len(grants) == 0 {
		fmt.Fprintln(stderr, " with no users: only NAUTILUS_TOKEN (and service tokens) work until auth.users names one")
	} else {
		fmt.Fprintln(stderr)
	}
	fmt.Fprintln(stderr, "With it, every request needs a credential: reads too, unless you set anonymous: viewer.")
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
