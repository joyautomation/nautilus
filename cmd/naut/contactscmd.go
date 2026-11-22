package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/internal/project"
)

const contactsUsage = `naut alarms contacts|secrets — who escalation pages, and the channels' credentials (#715)

Usage:
  naut alarms contacts [flags] list               each person's addresses (masked; --show prints them)
  naut alarms contacts [flags] set <person> [--email <a>] [--sms <+number>] [--voice <+number>]
                                                  set addresses; "-" clears one
  naut alarms contacts [flags] rm <person>        remove a person
  naut alarms secrets [flags] list                which credentials are set (never their values)
  naut alarms secrets [flags] set <key>           set one, read from stdin (a terminal: no echo)
  naut alarms secrets [flags] rm <key>

A person is a principal as the manifest's levels name one: a local user
name (alice) or oidc:<name>. Numbers are E.164 (+15550100). Secret keys:
email.password, sms.token, voice.token, webhook.header.

Flags:
  --project <dir>    the project whose nautilus.yaml says where the state
                     directory is (default ".")
  --state-dir <dir>  the controller's state directory, instead

Addresses and credentials never go in nautilus.yaml or Git. They live in
alarm-contacts.json and alarm-secrets.json in the controller's state
directory, beside users.json, mode 0600: auth.state-dir, else
$STATE_DIRECTORY (/var/lib/nautilus on a box naut deploy set up), else,
for a developer's checkout, $XDG_STATE_HOME/nautilus/<project>. Run on the
box with sudo; a running controller reads a change on its next page. A
secret is never taken as an argument (it would sit in the shell history
and the process list).

On the box, the built controller answers the same subcommands without
starting: sudo /opt/nautilus/current/controller alarms contacts --state-dir /var/lib/nautilus list
`

// runAlarmsContacts is naut alarms contacts|secrets in a project checkout.
func runAlarmsContacts(which string, args []string) int {
	return contactsMain(which, args, func(dir string) (*project.Manifest, string, error) {
		m, err := project.ReadManifest(os.DirFS(dir), "")
		return m, dir, err
	}, true, os.Getenv, os.Stdin, os.Stdout, os.Stderr)
}

// contactsMain is naut alarms contacts (which "contacts") or secrets
// (which "secrets"); load and devFallback are as usersMain takes them.
func contactsMain(which string, args []string, load func(dir string) (*project.Manifest, string, error), devFallback bool,
	getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("naut alarms "+which, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, contactsUsage) }
	projectDir := fs.String("project", ".", "project directory")
	stateDir := fs.String("state-dir", "", "the controller's state directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprint(stderr, contactsUsage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "naut alarms %s: %v\n", which, err)
		return 1
	}
	m, dir, err := load(*projectDir)
	if err != nil && *stateDir == "" {
		return fail(fmt.Errorf("%w (or pass --state-dir)", err))
	}
	sd, name := *stateDir, ""
	if m != nil {
		name = m.Name
		if sd == "" {
			sd = m.AuthStateDir()
		}
	}
	if *stateDir != "" {
		if err := outsideProject(sd, dir); err != nil {
			return fail(err)
		}
	}
	if sd, err = controllerStateDir("the alarm contacts", sd, name, dir, devFallback, getenv); err != nil {
		return fail(err)
	}
	by := osUser(getenv, os.Geteuid())
	cmd, rest := fs.Arg(0), fs.Args()[1:]
	if which == "secrets" {
		return secretsCmd(cmd, rest, oncall.SecretsPath(sd), by, stdin, stdout, stderr, fail)
	}
	return contactsCmd(cmd, rest, oncall.ContactsPath(sd), by, stdout, stderr, fail)
}

func contactsCmd(cmd string, rest []string, path, by string, stdout, stderr io.Writer, fail func(error) int) int {
	switch cmd {
	case "list":
		show := len(rest) == 1 && rest[0] == "--show"
		if len(rest) > 1 || len(rest) == 1 && !show {
			fmt.Fprint(stderr, contactsUsage)
			return 2
		}
		c, err := oncall.ReadContacts(path)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s\n", path)
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PERSON\tEMAIL\tSMS\tVOICE")
		for _, p := range c.Principals() {
			ct := c.Contacts[p]
			row := []string{p}
			for _, k := range oncall.Kinds {
				a := ct.Address(k)
				switch {
				case a == "":
					a = "-"
				case !show:
					a = mask(a)
				}
				row = append(row, a)
			}
			fmt.Fprintln(tw, strings.Join(row, "\t"))
		}
		tw.Flush()
		return 0
	case "set":
		if len(rest) < 3 || len(rest)%2 == 0 {
			fmt.Fprint(stderr, contactsUsage)
			return 2
		}
		who := strings.ToLower(rest[0])
		if strings.HasPrefix(rest[0], "oidc:") {
			who = rest[0]
		}
		if err := oncall.ValidPrincipal(who); err != nil {
			return fail(err)
		}
		set := map[string]string{}
		for i := 1; i < len(rest); i += 2 {
			k := strings.TrimPrefix(rest[i], "--")
			switch k {
			case oncall.Email, oncall.SMS, oncall.Voice:
			default:
				return fail(fmt.Errorf("%s: want --email, --sms or --voice", rest[i]))
			}
			set[k] = rest[i+1]
		}
		err := oncall.UpdateContacts(path, by, func(c *oncall.Contacts) error {
			ct := c.Contacts[who]
			if ct == nil {
				ct = &oncall.Contact{}
				c.Contacts[who] = ct
			}
			for k, v := range set {
				if v == "-" {
					v = ""
				}
				switch k {
				case oncall.Email:
					ct.Email = v
				case oncall.SMS:
					ct.SMS = v
				case oncall.Voice:
					ct.Voice = v
				}
			}
			return nil
		})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stderr, "%s: set %s. A running controller pages with it from its next send.\n", path, who)
		return 0
	case "rm":
		if len(rest) != 1 {
			fmt.Fprint(stderr, contactsUsage)
			return 2
		}
		who := rest[0]
		err := oncall.UpdateContacts(path, by, func(c *oncall.Contacts) error {
			if c.Contacts[who] == nil {
				return fmt.Errorf("%s has no contact here (naut alarms contacts list)", who)
			}
			delete(c.Contacts, who)
			return nil
		})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stderr, "%s: removed %s.\n", path, who)
		return 0
	}
	fmt.Fprintf(stderr, "naut alarms contacts: unknown subcommand %q\n\n%s", cmd, contactsUsage)
	return 2
}

func secretsCmd(cmd string, rest []string, path, by string, stdin io.Reader, stdout, stderr io.Writer, fail func(error) int) int {
	switch cmd {
	case "list":
		if len(rest) != 0 {
			fmt.Fprint(stderr, contactsUsage)
			return 2
		}
		s, err := oncall.ReadSecrets(path)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s\n", path)
		for _, k := range oncall.SecretKeys {
			state := "not set"
			if s.Secrets[k] != "" {
				state = "set"
			}
			fmt.Fprintf(stdout, "  %-16s %s\n", k, state)
		}
		return 0
	case "set", "rm":
		if len(rest) != 1 {
			fmt.Fprint(stderr, contactsUsage)
			return 2
		}
		key := rest[0]
		var v string
		if cmd == "set" {
			var err error
			if v, err = readSecret(stdin, stderr, key); err != nil {
				return fail(err)
			}
			if v == "" {
				return fail(errors.New("an empty secret: use naut alarms secrets rm to remove one"))
			}
		}
		err := oncall.UpdateSecrets(path, by, func(s *oncall.Secrets) error {
			if cmd == "rm" {
				if s.Secrets[key] == "" {
					return fmt.Errorf("%s is not set here", key)
				}
				delete(s.Secrets, key)
				return nil
			}
			s.Secrets[key] = v
			return nil
		})
		if err != nil {
			return fail(err)
		}
		verb := "set"
		if cmd == "rm" {
			verb = "removed"
		}
		fmt.Fprintf(stderr, "%s: %s %s.\n", path, verb, key)
		return 0
	}
	fmt.Fprintf(stderr, "naut alarms secrets: unknown subcommand %q\n\n%s", cmd, contactsUsage)
	return 2
}

// readSecret reads a secret from a terminal without echo, otherwise the
// first line of stdin.
func readSecret(stdin io.Reader, stderr io.Writer, key string) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(stderr, "%s: ", key)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", errors.New("no secret on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// mask shows enough of an address to tell people apart, not to use it:
// the first character and the domain of an email, the last two digits of
// a number.
func mask(a string) string {
	if at := strings.LastIndexByte(a, '@'); at > 0 {
		return a[:1] + "…@" + a[at+1:]
	}
	if len(a) > 2 {
		return "…" + a[len(a)-2:]
	}
	return "…"
}
