package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/siemens/webapi"
)

const siemensDiagUsage = `naut siemens diag — read a Siemens S7-1500's diagnostic buffer

Usage:
  naut siemens diag --host <cpu> --user <web user> --cert-sha256 <pin> [flags]

Reads the CPU's diagnostic buffer over the PLC Web API (DiagnosticBuffer.Browse:
firmware 3.1 or later, not on R/H CPUs). It reads and changes nothing. The web
user needs the diagnostics right (web-server.users[].rights: [diagnostics] in
the station spec). The password is never a flag: set NAUTILUS_SIEMENS_PASSWORD.

Entries print in the order the CPU sends them, one per line: the time (UTC),
incoming or outgoing, the event ID (text list/text) and the short text.

Flags:
  --host         CPU address (default: target.siemens.station.cpu.address)
  --user         Web server user (default: target.siemens.online.user)
  --cert-sha256  The CPU certificate's SHA-256 (default:
                 target.siemens.online.cert-sha256; see naut siemens cert)
  --project      A nautilus project directory with a target: siemens section,
                 for the defaults above (default: the current directory)
  --count        Entries to read (default 50)
  --lang         Text language, RFC 4647 (default en-US). The CPU answers in
                 the languages its project holds.
  --long         Also print each entry's long text and help text
  --status       Print only how many entries the buffer holds and when it
                 last changed (no entries; a cheap "did anything happen")
  --json         Print the read as JSON
`

// siemensDiagConfig is what diag needs, after flags and the project.
type siemensDiagConfig struct {
	host, user, pin, password string
	lang                      string
	count                     int
	long, status, json        bool
}

func runSiemensDiag(args []string) int {
	fs := flag.NewFlagSet("siemens diag", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, siemensDiagUsage) }
	host := fs.String("host", "", "CPU address")
	user := fs.String("user", "", "web server user")
	pin := fs.String("cert-sha256", "", "CPU certificate SHA-256")
	projectDir := fs.String("project", ".", "nautilus project directory")
	cfg := siemensDiagConfig{}
	fs.IntVar(&cfg.count, "count", 50, "entries to read")
	fs.StringVar(&cfg.lang, "lang", webapi.DefaultDiagLanguage, "text language")
	fs.BoolVar(&cfg.long, "long", false, "also print long and help texts")
	fs.BoolVar(&cfg.status, "status", false, "print only the buffer's status")
	fs.BoolVar(&cfg.json, "json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "naut siemens diag: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	cfg, err := resolveSiemensDiag(cfg, *host, *user, *pin, *projectDir, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut siemens diag:", err)
		return 2
	}
	client, err := webapi.New(cfg.host, webapi.Options{User: cfg.user, Password: cfg.password, CertSHA256: cfg.pin, Timeout: 30 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut siemens diag:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return siemensDiag(ctx, cfg, client, os.Stdout, os.Stderr)
}

// resolveSiemensDiag merges flags over the project's target.siemens and
// the environment.
func resolveSiemensDiag(c siemensDiagConfig, host, user, pin, projectDir string, getenv func(string) string) (siemensDiagConfig, error) {
	c.host, c.user, c.pin, c.password = host, user, pin, getenv("NAUTILUS_SIEMENS_PASSWORD")
	if projectDir != "" {
		m, err := project.ReadManifest(os.DirFS(projectDir), "")
		if err != nil && !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no nautilus.yaml") {
			return c, err
		}
		if err == nil {
			siemensOnlineDefaults(m, &c.host, &c.user, &c.pin)
		}
	}
	if err := checkSiemensOnline(c.host, c.user, c.password, c.pin); err != nil {
		return c, err
	}
	if !c.status && c.count < 1 {
		return c, fmt.Errorf("--count %d: read at least one entry, or use --status", c.count)
	}
	return c, nil
}

// siemensDiag reads the buffer and prints it. The Web API session is
// logged out on every path, so a read never keeps one of the CPU's few
// session slots.
func siemensDiag(ctx context.Context, cfg siemensDiagConfig, client *webapi.Client, stdout, stderr io.Writer) int {
	defer func() {
		lctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Logout(lctx); err != nil {
			fmt.Fprintln(stderr, "naut siemens diag: logout:", err)
		}
	}()
	var b webapi.DiagBuffer
	var err error
	if cfg.status {
		b, err = client.DiagnosticBufferStatus(ctx)
	} else {
		b, err = client.DiagnosticBuffer(ctx, cfg.lang, cfg.count)
	}
	if err != nil {
		fmt.Fprintln(stderr, "naut siemens diag:", err)
		if hint := diagHint(err, cfg.user); hint != "" {
			fmt.Fprintln(stderr, "  "+hint)
		}
		return 1
	}
	if cfg.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(b); err != nil {
			fmt.Fprintln(stderr, "naut siemens diag:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "diagnostic buffer of %s: %d of %d entries, last changed %s\n", cfg.host, b.Count, b.Max, b.LastModified)
	for _, e := range b.Entries {
		fmt.Fprintf(stdout, "%s  %-8s  %d/%d  %s\n", e.Timestamp, e.Status, e.Event.TextList, e.Event.Text, oneLine(e.ShortText))
		if cfg.long {
			for _, t := range []string{e.LongText, e.HelpText} {
				for _, l := range strings.Split(strings.TrimSpace(t), "\n") {
					if l = strings.TrimSpace(l); l != "" {
						fmt.Fprintln(stdout, "    "+l)
					}
				}
			}
		}
	}
	return 0
}

// diagHint names the fix for the refusals a diagnostic read expects.
func diagHint(err error, user string) string {
	switch webapi.Code(err) {
	case webapi.CodePermissionDenied:
		return fmt.Sprintf("the web user %q needs the diagnostics right (web-server.users[].rights: [diagnostics] in the station spec, then download)", user)
	case webapi.CodeMethodNotFound:
		return "this CPU's Web API has no DiagnosticBuffer.Browse: it needs firmware 3.1 or later, and R/H CPUs do not offer it"
	}
	return ""
}

// oneLine folds a text onto one line for the listing.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
