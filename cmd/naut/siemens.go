package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/auth"
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
  --root         A data block to serve, DB or DB=Prefix; repeatable. A DB's
                 members are served as plain names, or as Prefix.<name> with
                 a prefix. Default Tags (the global tags DB).
  --poll         Poll period (default 250ms)
  --listen       API address (default 127.0.0.1:8080). Anything but
                 loopback needs NAUTILUS_TOKEN set: a write here changes a
                 running PLC.
  --project      A nautilus project directory with a target: siemens section,
                 for the defaults above (default: the current directory, if
                 it has one)

Writes are open to non-browser clients and same-origin pages, like
` + "`naut run`" + `. NAUTILUS_TOKEN requires a bearer token; serve refuses to
listen anywhere but loopback without one.
`

// defaultSiemensListen is loopback: a write through serve changes a
// running PLC.
const defaultSiemensListen = "127.0.0.1:8080"

// isLoopback reports whether a listen address only accepts local
// connections. ":8080", "0.0.0.0:8080" and a LAN address do not.
func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

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
	token                     string // NAUTILUS_TOKEN, for the API
	roots                     []facade.Root
	poll                      time.Duration
	listen                    string

	// auth and services are the project's auth: block and its service
	// tokens' secrets (#708), enforced as under naut run; nil without one.
	auth     *auth.Policy
	services map[string]string
	// tls is the project's server.tls (or NAUTILUS_TLS_CERT/_KEY), nil
	// without one.
	tls *server.CertSource
}

// resolveSiemensServe merges flags over the project's target.siemens and
// the environment, and refuses what would make serve unsafe or useless.
func resolveSiemensServe(host, user, pin, projectDir string, roots []facade.Root, poll time.Duration, listen string,
	getenv func(string) string) (siemensServeConfig, error) {
	c := siemensServeConfig{host: host, user: user, pin: pin, roots: roots, poll: poll, listen: listen,
		password: getenv("NAUTILUS_SIEMENS_PASSWORD"), token: getenv("NAUTILUS_TOKEN")}
	var tlsCfg *project.TLSConfig // server.tls, when the project has one
	var tlsState, tlsName string
	if projectDir != "" {
		m, err := project.ReadManifest(os.DirFS(projectDir), "")
		if err != nil && !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no nautilus.yaml") {
			return c, err
		}
		if err == nil {
			// The project's auth: block decides the serve's writes, as
			// under naut run (#708).
			sopts := server.Options{AuthToken: c.token}
			if err := withAuth(&sopts, m.AuthPolicy(), getenv); err != nil {
				return c, fmt.Errorf("auth: %w", err)
			}
			c.auth, c.services = sopts.Auth, sopts.ServiceTokens
		}
		if err == nil {
			tlsCfg, tlsState, tlsName = m.Server.TLS, m.AuthStateDir(), m.Name
		}
		if err == nil && m.Target != nil && m.Target.Siemens != nil {
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
	// HTTPS: the project's server.tls, or NAUTILUS_TLS_CERT and _KEY with
	// or without a project.
	src, err := tlsSource(tlsSpecOf(tlsCfg, tlsState, tlsName, listen, true,
		func(s string) { fmt.Fprint(os.Stderr, "naut siemens serve: "+s) }), projectDir, getenv)
	if err != nil {
		return c, err
	}
	c.tls = src
	switch {
	case c.host == "":
		return c, errors.New("--host is required (or a project whose target.siemens names the CPU's address)")
	case c.user == "":
		return c, errors.New("--user is required: the Web API serves nothing anonymously here")
	case c.password == "":
		return c, errors.New("set NAUTILUS_SIEMENS_PASSWORD to the web user's password (it is never a flag)")
	case c.pin == "":
		return c, fmt.Errorf("--cert-sha256 is required to trust the CPU's self-signed certificate: run naut siemens cert --host %s and compare it with the CPU's", c.host)
	case !isLoopback(c.listen) && c.token == "":
		return c, fmt.Errorf("--listen %s accepts connections from other machines: set NAUTILUS_TOKEN first, since a write here changes a running PLC", c.listen)
	}
	if len(c.roots) == 0 {
		c.roots = []facade.Root{{DB: "Tags"}}
	}
	return c, nil
}

// siemensServeFlags are serve's flags, built in one place so a test can
// read their defaults.
type siemensServeFlags struct {
	host, user, pin, listen, project *string
	roots                            rootList
	poll                             *time.Duration
}

func newSiemensServeFlags() (*flag.FlagSet, *siemensServeFlags) {
	fs := flag.NewFlagSet("siemens serve", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, siemensServeUsage) }
	f := &siemensServeFlags{
		host:    fs.String("host", "", "CPU address"),
		user:    fs.String("user", "", "web server user"),
		pin:     fs.String("cert-sha256", "", "CPU certificate SHA-256"),
		poll:    fs.Duration("poll", 250*time.Millisecond, "poll period"),
		listen:  fs.String("listen", defaultSiemensListen, "API address"),
		project: fs.String("project", ".", "nautilus project directory"),
	}
	fs.Var(&f.roots, "root", "data block to serve (DB or DB=Prefix), repeatable")
	return fs, f
}

func runSiemensServe(args []string) int {
	fs, fl := newSiemensServeFlags()
	if err := fs.Parse(args); err != nil {
		return 2
	}
	host, user, pin, listen, projectDir, poll, roots := fl.host, fl.user, fl.pin, fl.listen, fl.project, fl.poll, fl.roots
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
	if cfg.auth != nil {
		fmt.Fprint(os.Stderr, authFacadeNotice)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveSiemens(ctx, cfg, client, os.Stdout, os.Stderr, nil)
}

// siemensFacadeOptions is the facade's configuration for cfg.
func siemensFacadeOptions(cfg siemensServeConfig, client *webapi.Client) facade.Options {
	return facade.Options{
		Client: client,
		Host:   cfg.host,
		Roots:  cfg.roots,
		Poll:   cfg.poll,
		Server: server.Options{AuthToken: cfg.token, Auth: cfg.auth, ServiceTokens: cfg.services, TLS: cfg.tls},
	}
}

// serveSiemens runs the facade until ctx ends. Whatever happens after the
// first browse, the Web API session is logged out before it returns, so a
// stopped serve never holds one of the CPU's few session slots. ready, when
// set, receives the API's address once it listens.
func serveSiemens(ctx context.Context, cfg siemensServeConfig, client *webapi.Client, stdout, stderr io.Writer, ready chan<- string) int {
	defer func() {
		lctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Logout(lctx); err != nil {
			fmt.Fprintln(stderr, "naut siemens serve: logout:", err)
		}
	}()
	fmt.Fprintf(stderr, "browsing %s...\n", cfg.host)
	bctx, cancel := context.WithTimeout(ctx, time.Minute)
	f, err := facade.New(bctx, siemensFacadeOptions(cfg, client))
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "naut siemens serve:", err)
		return 1
	}
	for _, s := range f.Skipped() {
		fmt.Fprintln(stderr, "  skipped", s)
	}
	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		fmt.Fprintln(stderr, "naut siemens serve:", err)
		return 1
	}
	ln, scheme := facadeListener(ln, cfg.tls, cfg.listen, "naut siemens serve", stderr)
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	go f.Run(runCtx)
	// Requests carry runCtx (derived from ctx), so stopping serve ends the
	// open /api/stream connections (VS Code keeps one) instead of Shutdown
	// waiting them out.
	srv := apiHTTPServer(f.Handler())
	srv.BaseContext = func(net.Listener) context.Context { return runCtx }
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	fmt.Fprintf(stdout, "nautilus · Siemens %s — %d tags on %s://%s — Ctrl+C to stop\n",
		cfg.host, len(f.Bindings()), scheme, displayAddr(ln.Addr()))
	if ready != nil {
		ready <- ln.Addr().String()
	}
	select {
	case err := <-served:
		fmt.Fprintln(stderr, "naut siemens serve:", err)
		return 1
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
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
