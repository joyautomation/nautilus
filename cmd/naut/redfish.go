package main

// `naut redfish` is the commissioning side of the Redfish driver
// (docs/design/it-drivers.md §8): import (a live BMC, or its recording →
// redfish_manifest.yaml + tags/redfish.yaml + hw_types.st), browse (read a
// resource live, or --record the whole service as a DMTF mockup
// directory), serve (stand in for a BMC from a recording) and tags
// (re-derive the tag file from a committed manifest).
//
// It mirrors `naut modbus` deliberately — one shape per protocol. The
// difference is where the device description comes from: Redfish is
// self-describing, so import reads the BMC (or a recording of it) instead
// of a hand-written map, and the recording is what makes that repeatable.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/redfish/codegen"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

const redfishUsage = `naut redfish — Redfish tools (server BMCs)

Usage:
  naut redfish import --host <url> --tag <NAME> [--mockup dir] [flags]
        Generate redfish_manifest.yaml + tags/redfish.yaml + hw_types.st for
        one server: from the live BMC, or (--mockup) from its recording.
        Byte-identical on re-run, and the same bytes from the BMC and from
        its recording. --host is always the address the manifest polls.
  naut redfish browse --host <url> [--path /redfish/v1/...] [--record dir]
        Live: print one resource and its links — the commissioning poke. With
        --record, walk the whole service into a DMTF mockup directory that
        import, serve and the tests consume (it carries serial numbers and
        addresses: review it before committing).
  naut redfish serve --mockup dir [flags]
        Serve a recording (or any DMTF mockup) as a stand-in BMC.
  naut redfish serve --mockup dir --manifest redfish_manifest.yaml
        --source NODE1 --from http://127.0.0.1:8087
        Stand in for the BMC from a plant simulation: every member the
        manifest binds is written into its resource from the plant's live
        tag (NODE1_Fan3.RPM), read backwards; the rest from the recording.
  naut redfish tags <redfish_manifest.yaml> [-o tags/redfish.yaml]
        Re-derive the tag file from a committed manifest.

Connection flags (import, browse):
  --host           BMC base URL: https://bmc1 (no scheme means https;
                   http:// for a mockup)
  --user           account name
  --password-env   environment variable holding the password
  --password-file  file holding the password (a mounted Secret)
  --insecure       accept the BMC's self-signed certificate unverified
  --ca-file        PEM file to verify the BMC's certificate against
  --timeout        per-request timeout (default 10s)

Import flags:
  --tag        device root: the Server tag, the source id, the children's
               prefix (NODE1 → NODE1_Fan1, NODE1_PSU1, NODE1_Temp_CPU)
  --mockup     read this recording instead of the live BMC
  --system     Systems member Id, when the service has several
  --chassis    Chassis member Id (default: the one the system links)
  --out        output directory (default ".")
  --tags-out   tag file, relative to --out (default tags/redfish.yaml)
  --types-out  types file, relative to --out (default hw_types.st)

Browse flags:
  --path       resource to print (default /redfish/v1)
  --record     directory to write the recording to (must be empty)
  --max        resource cap for --record (default 2000)

Serve flags:
  --mockup        the recording to serve (required)
  --listen        listen address (default 127.0.0.1:8000)
  --auth          none (default) | session | basic — what the stand-in demands
  --user          account the stand-in accepts (with --auth)
  --password-env  variable holding the password it accepts
  --from          a plant controller's URL: serve its tags (polls /api/state)
  --manifest      the redfish_manifest.yaml the monitoring project polls with
                  (required with --from: the bindings, read backwards)
  --source        the manifest source this BMC is (default: the only one);
                  --listen defaults to its host
  --every         how often to read the plant (default 1s)
                  The root tag's Online false takes the BMC off the network.
`

func runRedfish(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, redfishUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runRedfishImport(args[1:])
	case "browse":
		return runRedfishBrowse(args[1:])
	case "serve":
		return runRedfishServe(args[1:])
	case "tags":
		return runRedfishTags(args[1:])
	case "-h", "--help", "help":
		fmt.Print(redfishUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "naut redfish: unknown subcommand %q\n\n%s", args[0], redfishUsage)
		return 2
	}
}

// connFlags are the connection flags import and browse share.
type connFlags struct {
	host, user, passEnv, passFile, caFile *string
	insecure                              *bool
	timeout                               *time.Duration
}

func addConnFlags(fs *flag.FlagSet) connFlags {
	return connFlags{
		host:     fs.String("host", "", "BMC base URL"),
		user:     fs.String("user", "", "account name"),
		passEnv:  fs.String("password-env", "", "environment variable holding the password"),
		passFile: fs.String("password-file", "", "file holding the password"),
		insecure: fs.Bool("insecure", false, "accept the BMC's certificate unverified"),
		caFile:   fs.String("ca-file", "", "PEM CA to verify the BMC against"),
		timeout:  fs.Duration("timeout", redfish.DefaultTimeout, "per-request timeout"),
	}
}

func (c connFlags) source(id string) redfish.Source {
	return redfish.Source{
		ID: id, Host: *c.host, User: *c.user, PasswordEnv: *c.passEnv, PasswordFile: *c.passFile,
		TLS: redfish.TLS{Insecure: *c.insecure, CAFile: *c.caFile}, Timeout: *c.timeout,
	}
}

// client builds the live transport, validating the flags the way the
// manifest would be validated.
func (c connFlags) client() (*redfish.Client, error) {
	s := c.source("BMC")
	if err := (redfish.Manifest{Sources: []redfish.Source{s}}).Validate(); err != nil {
		return nil, err
	}
	return redfish.NewClient(s)
}

// ── import ───────────────────────────────────────────────────────────────

func runRedfishImport(args []string) int {
	fs := flag.NewFlagSet("redfish import", flag.ContinueOnError)
	conn := addConnFlags(fs)
	tag := fs.String("tag", "", "device root tag / source id")
	mockDir := fs.String("mockup", "", "recording to import instead of the live BMC")
	system := fs.String("system", "", "Systems member Id")
	chassis := fs.String("chassis", "", "Chassis member Id")
	outDir := fs.String("out", ".", "output directory")
	tagsOut := fs.String("tags-out", "tags/redfish.yaml", "tag file, relative to --out")
	typesOut := fs.String("types-out", "hw_types.st", "types file, relative to --out")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *conn.host == "" || *tag == "" {
		fmt.Fprintln(os.Stderr, "naut redfish import: --host and --tag are required (--host is what the manifest polls, even with --mockup)")
		return 2
	}
	ctx := context.Background()
	var g codegen.Getter
	if *mockDir != "" {
		tree, err := mockup.LoadDir(*mockDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut redfish import:", err)
			return 1
		}
		g = codegen.TreeGetter(tree)
	} else {
		cl, err := conn.client()
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut redfish import:", err)
			return 1
		}
		defer cl.Close(ctx)
		g = codegen.FetcherGetter(cl)
	}
	out, err := codegen.Import(ctx, g, codegen.Options{
		Tag: *tag, Host: *conn.host, User: *conn.user, PasswordEnv: *conn.passEnv, PasswordFile: *conn.passFile,
		Insecure: *conn.insecure, CAFile: *conn.caFile, System: *system, Chassis: *chassis,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish import:", err)
		return 1
	}
	// The header names the generating command without --host/--mockup: the
	// live BMC and its recording must give the same bytes.
	command := "naut redfish import --tag " + *tag
	if *system != "" {
		command += " --system " + *system
	}
	if *chassis != "" {
		command += " --chassis " + *chassis
	}
	tagsYAML, err := codegen.TagsYAML(out.Manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish import:", err)
		return 1
	}
	typesST, err := codegen.TypesST()
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish import:", err)
		return 1
	}
	files := []struct {
		path string
		body []byte
	}{
		{filepath.Join(*outDir, "redfish_manifest.yaml"), codegen.ManifestYAML(out.Manifest, command)},
		{filepath.Join(*outDir, *tagsOut), tagsYAML},
		{filepath.Join(*outDir, *typesOut), typesST},
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut redfish import:", err)
			return 1
		}
		if err := os.WriteFile(f.path, f.body, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "naut redfish import:", err)
			return 1
		}
	}
	for _, n := range out.Notes {
		fmt.Println("note:", n)
	}
	fmt.Printf("wrote %s (%s; %d tags)\n", files[0].path, out.Generation, len(out.Manifest.Tags))
	fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", files[1].path, *tagsOut)
	fmt.Printf("wrote %s — the IT-hardware types the tags use\n", files[2].path)
	return 0
}

// ── tags ─────────────────────────────────────────────────────────────────

func runRedfishTags(args []string) int {
	fs := flag.NewFlagSet("redfish tags", flag.ContinueOnError)
	out := fs.String("out", "tags/redfish.yaml", "output tag file")
	fs.StringVar(out, "o", "tags/redfish.yaml", "output tag file (alias for --out)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut redfish tags [-o tags/redfish.yaml] <redfish_manifest.yaml>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish tags:", err)
		return 1
	}
	m, err := redfish.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut redfish tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	if err := m.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "naut redfish tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	body, err := codegen.TagsYAML(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish tags:", err)
		return 1
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut redfish tags:", err)
			return 1
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d struct tags) — compose it with `tag-files: [%s]`\n", *out, len(m.Tags), *out)
	return 0
}

// ── browse ───────────────────────────────────────────────────────────────

func runRedfishBrowse(args []string) int {
	fs := flag.NewFlagSet("redfish browse", flag.ContinueOnError)
	conn := addConnFlags(fs)
	path := fs.String("path", mockup.Root, "resource to print")
	record := fs.String("record", "", "directory to record the whole service into")
	maxRes := fs.Int("max", 2000, "resource cap for --record")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *conn.host == "" {
		fmt.Fprintln(os.Stderr, "naut redfish browse: --host is required")
		return 2
	}
	cl, err := conn.client()
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish browse:", err)
		return 1
	}
	ctx := context.Background()
	defer cl.Close(ctx)
	if *record != "" {
		return redfishRecord(ctx, cl, *record, *maxRes, os.Stdout)
	}
	return redfishBrowse(ctx, cl, *path, os.Stdout)
}

// redfishBrowse prints one resource, indented, then what it links to and
// the scalar readings a binding could name — `path:` strings ready to paste.
func redfishBrowse(ctx context.Context, f redfish.Fetcher, path string, w io.Writer) int {
	t0 := time.Now()
	resp, err := f.Get(ctx, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish browse:", err)
		return 1
	}
	rtt := time.Since(t0)
	if !resp.OK() {
		fmt.Fprintf(os.Stderr, "naut redfish browse: %s: HTTP %d %s\n", path, resp.Status, strings.TrimSpace(string(resp.Body)))
		return 1
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, resp.Body, "", "  "); err != nil {
		fmt.Fprintf(os.Stderr, "naut redfish browse: %s: not JSON: %v\n", path, err)
		return 1
	}
	fmt.Fprintln(w, buf.String())
	doc, _ := mockup.Decode(resp.Body)
	var links []string
	for _, l := range mockup.Links(doc) {
		if l != path && !strings.Contains(l, "#") {
			links = append(links, l)
		}
	}
	sort.Strings(links)
	links = dedupe(links)
	if len(links) > 0 {
		fmt.Fprintln(w, "\nlinks:")
		for _, l := range links {
			fmt.Fprintln(w, "  "+l)
		}
	}
	if c, ok := f.(*redfish.Client); ok {
		fmt.Fprintf(w, "\n%s answered in %s (auth: %s)\n", path, rtt.Round(time.Millisecond), c.AuthMode())
	}
	return 0
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func redfishRecord(ctx context.Context, f redfish.Fetcher, dir string, maxRes int, w io.Writer) int {
	tree, notes, err := codegen.Record(ctx, f, codegen.RecordOptions{Max: maxRes})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish browse --record:", err)
		return 1
	}
	if err := tree.WriteDir(dir); err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish browse --record:", err)
		return 1
	}
	for _, n := range notes {
		fmt.Fprintln(w, "note:", n)
	}
	fmt.Fprintf(w, "recorded %d resources to %s — `naut redfish import --mockup %s` reads it; review it before committing (serials, addresses)\n", len(tree), dir, dir)
	return 0
}

// ── serve ────────────────────────────────────────────────────────────────

func runRedfishServe(args []string) int {
	fs := flag.NewFlagSet("redfish serve", flag.ContinueOnError)
	dir := fs.String("mockup", "", "recording to serve")
	listen := fs.String("listen", "127.0.0.1:8000", "listen address")
	auth := fs.String("auth", "none", "none | session | basic")
	user := fs.String("user", "", "account the stand-in accepts")
	passEnv := fs.String("password-env", "", "variable holding the password it accepts")
	from := fs.String("from", "", "plant controller URL to serve tags from")
	manifestPath := fs.String("manifest", "", "the monitoring project's redfish_manifest.yaml")
	sourceID := fs.String("source", "", "which manifest source this BMC is")
	every := fs.Duration("every", time.Second, "plant poll period")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "naut redfish serve: --mockup is required")
		return 2
	}
	var plantM *redfish.Manifest
	var plantSrc string
	if *from != "" || *manifestPath != "" {
		if *from == "" || *manifestPath == "" {
			fmt.Fprintln(os.Stderr, "naut redfish serve: --from and --manifest go together")
			return 2
		}
		m, src, host, err := loadRedfishPlant(*manifestPath, *sourceID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut redfish serve:", err)
			return 1
		}
		plantM, plantSrc = &m, src
		listenSet := false
		fs.Visit(func(f *flag.Flag) { listenSet = listenSet || f.Name == "listen" })
		if !listenSet {
			*listen = host
		}
	}
	srv, err := startRedfishServe(*dir, *listen, *auth, *user, *passEnv, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut redfish serve:", err)
		return 1
	}
	defer srv.Stop()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if plantM != nil {
		p, err := redfish.NewPlant(srv, *plantM, plantSrc)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut redfish serve:", err)
			return 1
		}
		p.Log = slog.New(slog.NewTextHandler(os.Stdout, nil))
		fmt.Printf("serving %d bound member(s) of %s from %s (every %s)\n", len(p.Feeds), plantSrc, *from, *every)
		go p.Run(ctx, redfish.StateFetcher(*from, plantM.TagPatterns(plantSrc)), *every)
	}
	<-ctx.Done()
	return 0
}

// loadRedfishPlant reads --manifest and resolves --source (default: the
// only one) and the address its host: names, which --listen defaults to.
func loadRedfishPlant(path, source string) (redfish.Manifest, string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return redfish.Manifest{}, "", "", err
	}
	m, err := redfish.ParseManifest(raw)
	if err != nil {
		return redfish.Manifest{}, "", "", fmt.Errorf("%s: %w", path, err)
	}
	if source == "" {
		if len(m.Sources) != 1 {
			ids := make([]string, len(m.Sources))
			for i, s := range m.Sources {
				ids[i] = s.ID
			}
			return m, "", "", fmt.Errorf("%s has %d sources (%s): name one with --source", path, len(m.Sources), strings.Join(ids, ", "))
		}
		source = m.Sources[0].ID
	}
	for _, s := range m.Sources {
		if s.ID == source {
			u, err := url.Parse(s.Host)
			if err != nil || u.Host == "" {
				return m, "", "", fmt.Errorf("%s: source %s: host %q is not a URL", path, source, s.Host)
			}
			return m, source, u.Host, nil
		}
	}
	return m, "", "", fmt.Errorf("%s: no source %q", path, source)
}

// startRedfishServe stands the mockup up; package-level so the test drives
// it in-process.
func startRedfishServe(dir, listen, auth, user, passEnv string, out io.Writer) (*mockup.Server, error) {
	tree, err := mockup.LoadDir(dir)
	if err != nil {
		return nil, err
	}
	opts := mockup.Options{User: user}
	switch auth {
	case "", "none":
	case "session":
		opts.Auth = mockup.AuthSession
	case "basic":
		opts.Auth = mockup.AuthBasic
	default:
		return nil, fmt.Errorf("--auth %q (want none, session or basic)", auth)
	}
	if opts.Auth != mockup.AuthNone {
		if user == "" || passEnv == "" {
			return nil, errors.New("--auth needs --user and --password-env")
		}
		p, ok := os.LookupEnv(passEnv)
		if !ok {
			return nil, fmt.Errorf("--password-env %s is not set", passEnv)
		}
		opts.Password = p
	}
	srv := mockup.New(tree, opts)
	if err := srv.Start(listen); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "serving %s (%d resources) at %s — a source's host: %s\n", dir, len(tree), srv.URL(), srv.URL())
	return srv, nil
}
