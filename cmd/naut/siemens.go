package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/siemens/facade"
	"github.com/joyautomation/nautilus/siemens/webapi"
)

const siemensUsage = `naut siemens — a Siemens S7-1500 as a nautilus target (experimental)

Usage:
  naut siemens serve --host <cpu> [flags]   serve the CPU on the nautilus runtime API
  naut siemens cert  --host <cpu>           print the CPU's certificate SHA-256, to pin

See docs/design/siemens-ide.md.
`

func runSiemens(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, siemensUsage)
		return 2
	}
	switch args[0] {
	case "serve":
		return runSiemensServe(args[1:])
	case "cert":
		return runSiemensCert(args[1:])
	case "-h", "--help", "help":
		fmt.Print(siemensUsage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "naut siemens: unknown command %q\n\n%s", args[0], siemensUsage)
	return 2
}

const siemensServeUsage = `naut siemens serve — a Siemens S7-1500, served as a nautilus runtime

Usage:
  naut siemens serve --host <cpu> --user <web user> --cert-sha256 <pin> [flags]

Browses the CPU over the PLC Web API (JSON-RPC over HTTPS, no licence, no
Siemens software) and serves its tags on the nautilus runtime API, so VS
Code's live values, hover, Live Values panel and set-value work against a
running CPU. Point the extension at it with the nautilus.runtimeUrl setting.

The CPU's web server must be on, with a user that may read (and, to set
values, write); the station spec in target.siemens turns both on. The
password is never a flag: set NAUTILUS_SIEMENS_PASSWORD.

A write from the editor goes to the CPU, and the value you then see is what
the next poll read back. Force is refused: the Web API writes but does not
force.

Flags:
  --host         CPU address (default: target.siemens.station.cpu.address)
  --user         Web server user (default: target.siemens.online.user)
  --cert-sha256  The CPU certificate's SHA-256, to pin it (default:
                 target.siemens.online.cert-sha256). Get it with
                 naut siemens cert --host <cpu> and compare it with the CPU's.
  --root         A data block to serve, DB or DB=Prefix; repeatable. Default
                 Tags (the global tags DB, as plain names). Main_DB=Main
                 serves program Main's variables as Main.<name>.
  --poll         Poll period (default 250ms)
  --listen       API address (default 127.0.0.1:8080)
  --project      A nautilus project directory with a target: siemens section,
                 for the defaults above (default: the current directory, if
                 it has one)

Writes are open to non-browser clients and same-origin pages, like
` + "`naut run`" + `. Set NAUTILUS_TOKEN to require a bearer token, and do,
before listening anywhere but loopback: a write here changes a running PLC.
`

// rootList is the repeatable --root flag.
type rootList []facade.Root

func (r *rootList) String() string { return fmt.Sprint(*r) }

func (r *rootList) Set(v string) error {
	root, err := parseRoot(v)
	if err != nil {
		return err
	}
	*r = append(*r, root)
	return nil
}

// parseRoot reads DB or DB=Prefix.
func parseRoot(v string) (facade.Root, error) {
	db, prefix, _ := strings.Cut(v, "=")
	db, prefix = strings.TrimSpace(db), strings.TrimSpace(prefix)
	if db == "" || strings.ContainsAny(db, `".`) || strings.ContainsAny(prefix, `" `) {
		return facade.Root{}, fmt.Errorf("--root %q: want DB or DB=Prefix, with an unquoted DB name", v)
	}
	return facade.Root{DB: db, Prefix: prefix}, nil
}

// siemensServeConfig is what serve needs, after flags and the project.
type siemensServeConfig struct {
	host, user, pin, password string
	roots                     []facade.Root
	poll                      time.Duration
	listen                    string
}

// resolveSiemensServe merges flags over the project's target.siemens and
// the environment, and refuses what would make serve unsafe or useless.
func resolveSiemensServe(host, user, pin, projectDir string, roots []facade.Root, poll time.Duration, listen string,
	getenv func(string) string) (siemensServeConfig, error) {
	c := siemensServeConfig{host: host, user: user, pin: pin, roots: roots, poll: poll, listen: listen,
		password: getenv("NAUTILUS_SIEMENS_PASSWORD")}
	if projectDir != "" {
		m, err := project.ReadManifest(os.DirFS(projectDir), "")
		switch {
		case err != nil && !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no nautilus.yaml"):
			return c, err
		case err == nil && m.Target != nil && m.Target.Siemens != nil:
			t := m.Target.Siemens
			if c.host == "" {
				c.host = t.Station.CPU.Address
			}
			if o := t.Online; o != nil {
				if c.user == "" {
					c.user = o.User
				}
				if c.pin == "" {
					c.pin = o.CertSHA256
				}
			}
		}
	}
	switch {
	case c.host == "":
		return c, errors.New("--host is required (or a project whose target.siemens names the CPU's address)")
	case c.user == "":
		return c, errors.New("--user is required: the Web API serves nothing anonymously here")
	case c.password == "":
		return c, errors.New("set NAUTILUS_SIEMENS_PASSWORD to the web user's password (it is never a flag)")
	case c.pin == "":
		return c, fmt.Errorf("--cert-sha256 is required to trust the CPU's self-signed certificate: run naut siemens cert --host %s and compare it with the CPU's", c.host)
	}
	if len(c.roots) == 0 {
		c.roots = []facade.Root{{DB: "Tags"}}
	}
	return c, nil
}

func runSiemensServe(args []string) int {
	fs := flag.NewFlagSet("siemens serve", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, siemensServeUsage) }
	host := fs.String("host", "", "CPU address")
	user := fs.String("user", "", "web server user")
	pin := fs.String("cert-sha256", "", "CPU certificate SHA-256")
	var roots rootList
	fs.Var(&roots, "root", "data block to serve (DB or DB=Prefix), repeatable")
	poll := fs.Duration("poll", 250*time.Millisecond, "poll period")
	listen := fs.String("listen", "127.0.0.1:8080", "API address")
	projectDir := fs.String("project", ".", "nautilus project directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := resolveSiemensServe(*host, *user, *pin, *projectDir, roots, *poll, *listen, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut siemens serve:", err)
		return 2
	}
	client, err := webapi.New(cfg.host, webapi.Options{User: cfg.user, Password: cfg.password, CertSHA256: cfg.pin})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut siemens serve:", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "browsing %s...\n", cfg.host)
	bctx, cancel := context.WithTimeout(ctx, time.Minute)
	f, err := facade.New(bctx, facade.Options{
		Client: client,
		Host:   cfg.host,
		Roots:  cfg.roots,
		Poll:   cfg.poll,
		Server: server.Options{AuthToken: os.Getenv("NAUTILUS_TOKEN")},
	})
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut siemens serve:", err)
		return 1
	}
	for _, s := range f.Skipped() {
		fmt.Fprintln(os.Stderr, "  skipped", s)
	}
	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut siemens serve:", err)
		return 1
	}
	go f.Run(ctx)
	srv := &http.Server{Handler: f.Handler()}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = client.Logout(context.Background())
	}()
	fmt.Printf("nautilus · Siemens %s — %d tags on http://%s — Ctrl+C to stop\n",
		cfg.host, len(f.Bindings()), displayAddr(ln.Addr()))
	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "naut siemens serve:", err)
		return 1
	}
	return 0
}

func runSiemensCert(args []string) int {
	fs := flag.NewFlagSet("siemens cert", flag.ContinueOnError)
	host := fs.String("host", "", "CPU address (host or host:port)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "naut siemens cert: --host is required")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sum, err := webapi.CertificateSHA256(ctx, *host)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut siemens cert:", err)
		return 1
	}
	fmt.Println(sum)
	fmt.Fprintln(os.Stderr, "compare this with the certificate the CPU shows (its web server, or TIA's certificate manager) before pinning it")
	return 0
}
