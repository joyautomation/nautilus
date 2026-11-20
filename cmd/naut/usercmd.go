package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/project"
)

const usersUsage = `naut users — the local users of a controller with an auth: block (#708)

Usage:
  naut users [flags] add <name>               add a user (asks for the password twice)
  naut users [flags] passwd <name>            change a user's password
  naut users [flags] disable <name>           no sign-in, and none of the user's tokens works
  naut users [flags] enable <name>
  naut users [flags] list                     the users here, and the manifest's users
                                              with no password here
  naut users [flags] token <name>             issue a personal access token (shown once)
  naut users [flags] revoke <name> <token-id> revoke one
  naut users [flags] signout <name>           end every browser session of a local user
                                              or an OIDC name (oidc:<name> when both exist)
  naut users [flags] unpin <name>             forget which account at the OIDC provider
                                              an OIDC name belongs to (auth.oidc)

Flags:
  --project <dir>    the project whose nautilus.yaml says where the users
                     file lives (default ".")
  --state-dir <dir>  the controller's state directory, instead
  --file <path>      the users file itself, instead

What a user may do is the manifest's auth: users: block, reviewed in git.
Their passwords and tokens live only on the controller, in users.json in
its state directory: auth.state-dir, else $STATE_DIRECTORY (systemd's
StateDirectory=, /var/lib/nautilus on a box naut deploy set up), else, for
a developer's checkout, $XDG_STATE_HOME/nautilus/<project>. Never inside
the project. Run on the box (with sudo there); a running controller picks
a change up within a second and journals it (users.change) by your login.

On the box, the built controller answers the same subcommands without
starting: sudo /opt/nautilus/current/controller users --state-dir /var/lib/nautilus list

A password is read from the terminal without echo, twice. When stdin is
not a terminal its first line is the password, for provisioning scripts.
A personal access token is sent as Authorization: Bearer <token> (the VS
Code extension's nautilus.token setting takes one). An OIDC name (auth.oidc)
gets none: a token would outlive the account's removal at the provider.

An OIDC name is pinned, on its first sign-in, to the account it came from
(oidc-pins.json beside the users file); a sign-in by another account under
that name is refused. When the account was re-created at the provider,
naut users unpin <name> lets the next sign-in pin it afresh. Sign-outs and
pins take effect within a second on a running controller. On a redundant
pair, sessions live on the leader: run signout on the leader.
`

// usersFile is where a controller keeps its users file: stateDir (the
// manifest's auth.state-dir), else $STATE_DIRECTORY, else, outside
// systemd and when devFallback allows it, the user's
// $XDG_STATE_HOME/nautilus/<name>. Never inside projectDir: a deploy
// replaces the project, and hashes must not reach git.
func usersFile(stateDir, name, projectDir string, devFallback bool, getenv func(string) string) (string, error) {
	sd, err := controllerStateDir("the users file", stateDir, name, projectDir, devFallback, getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(sd, users.FileName), nil
}

// controllerStateDir is the controller's state directory, for what (the
// users file, a self-signed certificate): stateDir (auth.state-dir), else
// $STATE_DIRECTORY, else, outside systemd and when devFallback allows it,
// $XDG_STATE_HOME/nautilus/<name>. Never inside projectDir.
func controllerStateDir(what, stateDir, name, projectDir string, devFallback bool, getenv func(string) string) (string, error) {
	sd := stateDir
	if sd == "" {
		sd, _, _ = strings.Cut(getenv("STATE_DIRECTORY"), ":")
	}
	if sd == "" {
		if getenv("INVOCATION_ID") != "" || !devFallback {
			return "", fmt.Errorf("no state directory for %s: set auth.state-dir in nautilus.yaml, or run under systemd with StateDirectory= (naut deploy does: /var/lib/nautilus)", what)
		}
		base := getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("no state directory for %s: %w; set auth.state-dir", what, err)
			}
			base = filepath.Join(home, ".local", "state")
		}
		if name == "" {
			name = "nautilus"
		}
		sd = filepath.Join(base, "nautilus", name)
	}
	sd, err := filepath.Abs(sd)
	if err != nil {
		return "", err
	}
	if err := outsideProject(sd, projectDir); err != nil {
		return "", err
	}
	return sd, nil
}

// outsideProject refuses a state directory inside projectDir: a deploy
// replaces the project, and the users file's hashes must never reach git.
func outsideProject(sd, projectDir string) error {
	if projectDir == "" {
		return nil
	}
	sd, err := filepath.Abs(sd)
	if err != nil {
		return err
	}
	proj, err := filepath.Abs(projectDir)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(proj, sd); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("state directory %s is inside the project %s: what it keeps (password hashes, a private key) must never reach git; set auth.state-dir outside it", sd, proj)
	}
	return nil
}

// runUsers is naut users in a project checkout.
func runUsers(args []string) int {
	return usersMain(args, func(dir string) (*project.Manifest, string, error) {
		m, err := project.ReadManifest(os.DirFS(dir), "")
		return m, dir, err
	}, true, os.Getenv, os.Stdin, os.Stdout, os.Stderr)
}

// usersMain is naut users: load gives the manifest (and the project
// directory, "" for a built controller's embedded one) for --project;
// devFallback allows the $XDG_STATE_HOME state directory.
func usersMain(args []string, load func(dir string) (*project.Manifest, string, error), devFallback bool,
	getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("naut users", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usersUsage) }
	projectDir := fs.String("project", ".", "project directory")
	stateDir := fs.String("state-dir", "", "the controller's state directory")
	file := fs.String("file", "", "the users file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprint(stderr, usersUsage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "naut users:", err)
		return 1
	}
	m, dir, err := load(*projectDir)
	if err != nil && *file == "" && *stateDir == "" {
		return fail(fmt.Errorf("%w (or pass --state-dir or --file)", err))
	}
	path := *file
	if path == "" {
		sd, name := *stateDir, ""
		if m != nil {
			name = m.Name
			if sd == "" {
				sd = m.AuthStateDir()
			}
		}
		if path, err = usersFile(sd, name, dir, devFallback, getenv); err != nil {
			return fail(err)
		}
	} else if err := outsideProject(filepath.Dir(path), dir); err != nil {
		return fail(err)
	}
	cmd, rest := fs.Arg(0), fs.Args()[1:]
	want := map[string]int{"add": 1, "passwd": 1, "disable": 1, "enable": 1, "list": 0, "token": 1, "revoke": 2, "signout": 1, "unpin": 1}
	n, ok := want[cmd]
	if !ok {
		fmt.Fprintf(stderr, "naut users: unknown subcommand %q\n\n%s", cmd, usersUsage)
		return 2
	}
	if len(rest) != n {
		fmt.Fprintf(stderr, "naut users %s takes %d argument(s)\n\n%s", cmd, n, usersUsage)
		return 2
	}
	var name string
	if n > 0 {
		name = strings.ToLower(rest[0])
	}
	if cmd == "list" {
		if err := users.CheckMode(path); err != nil {
			return fail(err)
		}
		f, err := users.Load(path)
		if err != nil {
			return fail(err)
		}
		listUsers(stdout, path, f, m)
		if err := users.CheckMode(users.PinsPath(path)); err != nil {
			return fail(err)
		}
		pins, err := users.LoadPins(users.PinsPath(path))
		if err != nil {
			return fail(err)
		}
		listPins(stdout, pins, m)
		return 0
	}
	switch cmd {
	case "signout":
		return signOutMain(path, name, m, getenv, stderr)
	case "unpin":
		name = strings.TrimPrefix(name, "oidc:")
		err := users.UpdatePins(users.PinsPath(path), osUser(getenv, os.Geteuid()), func(p *users.Pins) error { return p.Unpin(name) })
		if errors.Is(err, users.ErrNotPinned) {
			return fail(fmt.Errorf("oidc:%s is not pinned (naut users list shows the pins)", name))
		}
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stderr, "%s: unpinned oidc:%s. Its sessions end within a second on a running controller; its next sign-in pins it to whatever account at the provider it comes from.\n", users.PinsPath(path), name)
		return 0
	case "token":
		if why := oidcTokenRefusal(path, name, m); why != "" {
			return fail(errors.New(why))
		}
	}
	// A password is asked for before the file is read: the change is made
	// under the file's lock (users.Update), never on a copy read before a
	// prompt another run may have changed the file behind.
	var pw string
	if cmd == "add" || cmd == "passwd" {
		if pw, err = readPassword(stdin, stderr, name); err != nil {
			return fail(err)
		}
	}
	var tok, id string
	err = users.Update(path, osUser(getenv, os.Geteuid()), func(f *users.File) error {
		switch cmd {
		case "add":
			return f.Add(name, pw)
		case "passwd":
			return f.SetPassword(name, pw)
		case "disable", "enable":
			return f.SetDisabled(name, cmd == "disable")
		case "token":
			var err error
			tok, id, err = f.NewToken(name)
			return err
		case "revoke":
			return f.Revoke(name, rest[1])
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if cmd == "token" {
		fmt.Fprintf(stderr, "token %s for %s (shown once; naut users revoke %s %s ends it):\n", id, name, name, id)
		fmt.Fprintln(stdout, tok)
		fmt.Fprintf(stderr, "Next: send it as Authorization: Bearer <token>, e.g. curl -H \"Authorization: Bearer $TOKEN\" https://<controller>/api/me (VS Code: the nautilus.token setting). It acts as %s, with %s's roles, at once.\n", name, name)
		return 0
	}
	fmt.Fprintf(stderr, "%s: %s %s\n", path, cmd, name)
	if m != nil && cmd == "add" && !manifestNames(m)[name] {
		fmt.Fprintf(stderr, "naut users: warning: %s is not in nautilus.yaml's auth.users, so it holds no role and cannot sign in until it is\n", name)
	} else if cmd == "add" {
		fmt.Fprintf(stderr, "Next: %s signs in at /signin on the controller (over HTTPS, or from its own loopback); for a script or VS Code, naut users token %s. A running controller has the password within a second: no restart.\n", name, name)
	}
	return 0
}

// oidcTokenRefusal is why naut users token refuses name, "" when it does
// not: an OIDC name never gets a personal access token, which would keep
// working after the person's account is removed at the provider.
func oidcTokenRefusal(path, name string, m *project.Manifest) string {
	why := name + " is an OIDC name (auth.oidc): a personal access token would outlive the account's removal at the provider, so OIDC users get none; a script or VS Code uses a local user's token (naut users add) or a service token (auth.service-tokens)"
	if strings.HasPrefix(name, "oidc:") {
		return strings.TrimPrefix(why, "oidc:")
	}
	if f, err := users.Load(path); err == nil {
		if _, local := f.Users[name]; local {
			return ""
		}
	}
	if oidcNames(m)[name] {
		return why
	}
	if p, err := users.LoadPins(users.PinsPath(path)); err == nil {
		if _, pinned := p.Pins[name]; pinned {
			return why
		}
	}
	return ""
}

// oidcNames are the names auth.oidc.users grants roles.
func oidcNames(m *project.Manifest) map[string]bool {
	out := map[string]bool{}
	if m != nil && m.Auth != nil && m.Auth.OIDC != nil {
		for n := range m.Auth.OIDC.Users {
			out[strings.ToLower(n)] = true
		}
	}
	return out
}

// signOutMain is naut users signout: it records the sign-out beside the
// users file, which a running controller reads within a second and ends
// the principal's sessions (journalled, users.change). A bare name is
// the local user's, or the OIDC name's when there is no local user of it;
// with both, the name must say which (user:<name> or oidc:<name>).
func signOutMain(path, name string, m *project.Manifest, getenv func(string) string, stderr io.Writer) int {
	pr := name
	if !strings.HasPrefix(name, "user:") && !strings.HasPrefix(name, "oidc:") {
		local := manifestNames(m)[name]
		if f, err := users.Load(path); err == nil {
			_, inFile := f.Users[name]
			local = local || inFile
		}
		oidc := oidcNames(m)[name]
		if p, err := users.LoadPins(users.PinsPath(path)); err == nil {
			_, pinned := p.Pins[name]
			oidc = oidc || pinned
		}
		switch {
		case local && oidc:
			fmt.Fprintf(stderr, "naut users: %s is both a local user and an OIDC name: say which, naut users signout user:%s or oidc:%s\n", name, name, name)
			return 2
		case oidc:
			pr = "oidc:" + name
		default:
			pr = "user:" + name
		}
	}
	if err := users.SignOut(users.SignoutsPath(path), osUser(getenv, os.Geteuid()), pr, timeNow()); err != nil {
		fmt.Fprintln(stderr, "naut users:", err)
		return 1
	}
	fmt.Fprintf(stderr, "%s: signed out %s. A running controller ends its browser sessions within a second (its tokens, if any, still work: naut users revoke). On a redundant pair this reaches only this box: sessions live on the leader, so run it there.\n", users.SignoutsPath(path), pr)
	return 0
}

// timeNow is time.Now, a variable for tests.
var timeNow = time.Now

// listPins lists the OIDC pins, and the names auth.oidc.users grants that
// no sign-in has pinned yet.
func listPins(w io.Writer, p *users.Pins, m *project.Manifest) {
	names := p.Names()
	granted := oidcNames(m)
	if len(names) == 0 && len(granted) == 0 {
		return
	}
	fmt.Fprintln(w, "OIDC names pinned to an account (naut users unpin <name> forgets one):")
	if len(names) == 0 {
		fmt.Fprintln(w, "  (none yet: a name is pinned at its first sign-in)")
	}
	for _, n := range names {
		pin := p.Pins[n]
		fmt.Fprintf(w, "  oidc:%-30s sub %s at %s, since %s\n", n, pin.Subject, pin.Issuer, pin.Pinned.Format("2006-01-02 15:04Z"))
	}
	var waiting []string
	for n := range granted {
		if _, ok := p.Pins[n]; !ok {
			waiting = append(waiting, n)
		}
	}
	if len(waiting) > 0 {
		sort.Strings(waiting)
		fmt.Fprintf(w, "in auth.oidc.users, not signed in yet: %s\n", strings.Join(waiting, ", "))
	}
}

// manifestNames are the users the manifest's auth: block grants roles.
func manifestNames(m *project.Manifest) map[string]bool {
	out := map[string]bool{}
	if m != nil && m.Auth != nil {
		for n := range m.Auth.Users {
			out[strings.ToLower(n)] = true
		}
	}
	return out
}

func listUsers(w io.Writer, path string, f *users.File, m *project.Manifest) {
	fmt.Fprintf(w, "users file: %s\n", path)
	granted := manifestNames(m)
	tw := []string{}
	for _, n := range f.Names() {
		u := f.Users[n]
		state := "enabled"
		if u.Disabled {
			state = "disabled"
		}
		note := ""
		if m != nil && !granted[n] {
			note = "  (not in auth.users: no role, cannot sign in)"
		}
		ids := make([]string, 0, len(u.Tokens))
		for _, t := range u.Tokens {
			ids = append(ids, t.ID)
		}
		tok := "-"
		if len(ids) > 0 {
			tok = strings.Join(ids, ",")
		}
		tw = append(tw, fmt.Sprintf("  %-20s %-9s tokens: %s%s", n, state, tok, note))
	}
	if len(tw) == 0 {
		fmt.Fprintln(w, "  (no users)")
	}
	for _, l := range tw {
		fmt.Fprintln(w, l)
	}
	var missing []string
	for n := range granted {
		if _, ok := f.Users[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintf(w, "in auth.users with no password here: %s (naut users add <name>)\n", strings.Join(missing, ", "))
	}
}

// readPassword reads a new password: from a terminal twice without echo,
// otherwise the first line of stdin.
func readPassword(stdin io.Reader, stderr io.Writer, name string) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(stderr, "password for %s: ", name)
		a, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(stderr, "again: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		if string(a) != string(b) {
			return "", errors.New("the passwords differ")
		}
		return string(a), nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", errors.New("no password on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// osUser is who ran naut users, for the journal: the login behind sudo
// when there is one. SUDO_USER is believed only from root (euid 0), where
// sudo set it: anyone else can put any name in it.
func osUser(getenv func(string) string, euid int) string {
	if s := getenv("SUDO_USER"); s != "" && euid == 0 {
		return s
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}
