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
Code extension's nautilus.token setting takes one).
`

// usersFile is where a controller keeps its users file: stateDir (the
// manifest's auth.state-dir), else $STATE_DIRECTORY, else, outside
// systemd and when devFallback allows it, the user's
// $XDG_STATE_HOME/nautilus/<name>. Never inside projectDir: a deploy
// replaces the project, and hashes must not reach git.
func usersFile(stateDir, name, projectDir string, devFallback bool, getenv func(string) string) (string, error) {
	sd := stateDir
	if sd == "" {
		sd, _, _ = strings.Cut(getenv("STATE_DIRECTORY"), ":")
	}
	if sd == "" {
		if getenv("INVOCATION_ID") != "" || !devFallback {
			return "", errors.New("no state directory for the users file: set auth.state-dir in nautilus.yaml, or run under systemd with StateDirectory= (naut deploy does: /var/lib/nautilus)")
		}
		base := getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("no state directory for the users file: %w; set auth.state-dir", err)
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
	if projectDir != "" {
		proj, err := filepath.Abs(projectDir)
		if err != nil {
			return "", err
		}
		if rel, err := filepath.Rel(proj, sd); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("state directory %s is inside the project %s: the users file's hashes must never reach git; set auth.state-dir outside it", sd, proj)
		}
	}
	return filepath.Join(sd, users.FileName), nil
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
	}
	if err := users.CheckMode(path); err != nil {
		return fail(err)
	}
	f, err := users.Load(path)
	if err != nil {
		return fail(err)
	}
	cmd, rest := fs.Arg(0), fs.Args()[1:]
	want := map[string]int{"add": 1, "passwd": 1, "disable": 1, "enable": 1, "list": 0, "token": 1, "revoke": 2}
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
	switch cmd {
	case "list":
		listUsers(stdout, path, f, m)
		return 0
	case "add", "passwd":
		pw, err := readPassword(stdin, stderr, name)
		if err != nil {
			return fail(err)
		}
		if cmd == "add" {
			err = f.Add(name, pw)
		} else {
			err = f.SetPassword(name, pw)
		}
		if err != nil {
			return fail(err)
		}
	case "disable", "enable":
		if err := f.SetDisabled(name, cmd == "disable"); err != nil {
			return fail(err)
		}
	case "token":
		tok, id, err := f.NewToken(name)
		if err != nil {
			return fail(err)
		}
		if err := f.Save(path, osUser()); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stderr, "token %s for %s (shown once; naut users revoke %s %s ends it):\n", id, name, name, id)
		fmt.Fprintln(stdout, tok)
		return 0
	case "revoke":
		if err := f.Revoke(name, rest[1]); err != nil {
			return fail(err)
		}
	}
	if err := f.Save(path, osUser()); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stderr, "%s: %s %s\n", path, cmd, name)
	if m != nil && cmd == "add" && !manifestNames(m)[name] {
		fmt.Fprintf(stderr, "naut users: warning: %s is not in nautilus.yaml's auth.users, so it holds no role and cannot sign in until it is\n", name)
	}
	return 0
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
// when there is one.
func osUser() string {
	if s := os.Getenv("SUDO_USER"); s != "" {
		return s
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}
