package main

// `naut snmp` is the commissioning side of the SNMP driver
// (docs/design/it-drivers.md §8): browse (walk a live agent, names for what
// the profiles know, --record the fixture), import (a live agent or its
// recording → snmp_manifest.yaml + tags/snmp.yaml + hw_types.st), serve
// (stand in for the device from its recording), and tags (re-derive the tag
// file from a committed manifest).
//
// It mirrors `naut modbus` deliberately — one importer per protocol, one
// shape to learn. The difference is where the device map comes from: modbus
// reads datasheets, SNMP devices describe themselves, so import walks the
// agent (or a recording of it) and the profiles turn MIB rows into
// explicit per-member bindings. A live import and an import of its
// `browse --record` file write the same bytes.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/agent"
	"github.com/joyautomation/nautilus/snmp/codegen"
	"github.com/joyautomation/nautilus/snmp/profiles"
	"github.com/joyautomation/nautilus/snmp/walk"
)

const snmpUsage = `naut snmp — SNMP tools (switches, PDUs, UPSes)

Usage:
  naut snmp import --tag SW1 --host <ip> [--walk file.snmpwalk] [flags]
        Generate snmp_manifest.yaml + tags/snmp.yaml + hw_types.st. With
        --walk, offline from a recording; without, by walking the live agent
        at --host. The same device gives the same bytes either way.
  naut snmp browse --host <ip> [--oid 1.3.6.1.2.1.2] [--record file] [flags]
        Live: walk the agent and print every varbind, named where a profile
        knows the object (ifHCInOctets.3 = 812345678). --record writes the
        walk in the snmpwalk -One format import/serve/tests consume.
  naut snmp serve --walk file.snmpwalk [--listen 127.0.0.1:1161]
        Stand in for the device from its recording: a v2c agent answering
        Get/GetNext/GetBulk/Set, for naut run on a laptop and for tests.
  naut snmp serve --walk file.snmpwalk --manifest snmp_manifest.yaml
        --source SW1 --from http://127.0.0.1:8087
        Stand in for the device from a plant simulation: every OID the
        manifest binds answers the plant's live tag (SW1_Port25.OperUp),
        read backwards through the binding; the rest from the recording.
  naut snmp read --walk file.snmpwalk --manifest snmp_manifest.yaml
        [--source SW1]
        The manifest read forwards over a recording, offline: the real
        driver polls the walk once and prints the source's tags as JSON
        (rate members read 0 — one poll has no rate). The twin of
        serve --from; a plant simulation's device identities come from it.
  naut snmp tags <snmp_manifest.yaml> [-o tags/snmp.yaml] [--skip globs]
        Re-derive the tag file from a committed manifest — no device needed.

Import flags:
  --tag        Device prefix: source id, root tag, child prefix (required;
               letters and digits — SW1 gives SW1, SW1_Port01, SW1__Online)
  --host       Address the driver will poll, and the agent walked when no
               --walk is given (required)
  --walk       A recorded walk (snmpwalk -One format) instead of the live agent
  --port       UDP port (default 161)
  --profile    Force a profile: ` + "switch | pdu-cyberpower | ups-cyberpower | ups-rfc1628" + `
               (default: by sysObjectID, then by the walk's content)
  --ports      Interfaces by ifIndex, "1-24,26" or "all" (default: ifType 6)
  --out        Output directory (default ".")
  --tags-out   Tag file, relative to --out (default tags/snmp.yaml)
  --tags-skip  Comma-separated globs to leave OUT of the tag file
  --plan       Print the requests one poll would send

Credential and transport flags (import, browse):
  --version    2c (default) | 3
  --community-env / --community-file
               Where the v2c community is (import default SNMP_<TAG>_COMMUNITY,
               browse default SNMP_COMMUNITY). Never on the command line.
  --user --auth sha1|sha256|md5 --priv aes128|aes256|aes256c|des --context
  --auth-env / --auth-file, --priv-env / --priv-file
               v3 USM; the manifest names SNMP_<TAG>_AUTH / SNMP_<TAG>_PRIV
  --timeout    Per-request timeout (default 3s)
  --max-repetitions  GetBulk size (default 20)

Serve flags:
  --walk       The recording to serve (required)
  --listen     Listen address (default 127.0.0.1:1161)
  --community  The community it answers to (default public; a stand-in
               serves recordings, not secrets)
  --ramp       Move every non-zero ifHCInOctets/ifHCOutOctets counter at a
               few Mb/s, so InBps/OutBps read live on a bench
  --from       A plant controller's URL: serve its tags (polls /api/state)
  --manifest   The snmp_manifest.yaml the monitoring project polls with
               (required with --from: the bindings, read backwards)
  --source     The manifest source this agent is (default: the only one);
               --listen defaults to its host:port
  --every      How often to read the plant (default 1s)
               Rate members (InBps) steer counters; the root tag's Online
               false stops the agent answering (a dark switch).
`

func runSnmp(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, snmpUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runSnmpImport(args[1:])
	case "browse":
		return runSnmpBrowse(args[1:])
	case "serve":
		return runSnmpServe(args[1:])
	case "tags":
		return runSnmpTags(args[1:])
	case "read":
		return runSnmpRead(args[1:])
	case "-h", "--help", "help":
		fmt.Print(snmpUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "naut snmp: unknown subcommand %q\n\n%s", args[0], snmpUsage)
		return 2
	}
}

// liveFlags are the transport and credential flags import and browse share.
type liveFlags struct {
	port                   *int
	version                *string
	communityEnv, commFile *string
	user, auth, priv, ctx  *string
	authEnv, authFile      *string
	privEnv, privFile      *string
	timeout                *time.Duration
	maxRep                 *int
}

func addLiveFlags(fs *flag.FlagSet) *liveFlags {
	return &liveFlags{
		port:         fs.Int("port", snmp.DefaultPort, "UDP port"),
		version:      fs.String("version", snmp.V2c, "2c | 3"),
		communityEnv: fs.String("community-env", "", "variable holding the v2c community"),
		commFile:     fs.String("community-file", "", "file holding the v2c community"),
		user:         fs.String("user", "", "v3 user"),
		auth:         fs.String("auth", "", "v3 auth protocol: sha1 | sha256 | md5"),
		priv:         fs.String("priv", "", "v3 priv protocol: aes128 | aes256 | aes256c | des"),
		ctx:          fs.String("context", "", "v3 context name"),
		authEnv:      fs.String("auth-env", "", "variable holding the v3 auth pass phrase"),
		authFile:     fs.String("auth-file", "", "file holding the v3 auth pass phrase"),
		privEnv:      fs.String("priv-env", "", "variable holding the v3 priv pass phrase"),
		privFile:     fs.String("priv-file", "", "file holding the v3 priv pass phrase"),
		timeout:      fs.Duration("timeout", snmp.DefaultTimeout, "per-request timeout"),
		maxRep:       fs.Int("max-repetitions", snmp.DefaultMaxRepetitions, "GetBulk max-repetitions"),
	}
}

// source builds the Source a live command dials. defaultEnv names the
// community variable when neither --community-env nor --community-file is
// given (and, for v3, the _AUTH/_PRIV variables).
func (l *liveFlags) source(id, host, defaultEnv string) snmp.Source {
	s := snmp.Source{
		ID: id, Host: host, Port: *l.port, Version: *l.version,
		CommunityEnv: *l.communityEnv, CommunityFile: *l.commFile,
		User: *l.user, Auth: *l.auth, Priv: *l.priv, Context: *l.ctx,
		AuthEnv: *l.authEnv, AuthFile: *l.authFile, PrivEnv: *l.privEnv, PrivFile: *l.privFile,
		Timeout: *l.timeout, MaxRepetitions: *l.maxRep,
	}
	if s.Version == snmp.V2c && s.CommunityEnv == "" && s.CommunityFile == "" {
		s.CommunityEnv = defaultEnv + "COMMUNITY"
	}
	if s.Version == snmp.V3 {
		if s.Auth != "" && s.AuthEnv == "" && s.AuthFile == "" {
			s.AuthEnv = defaultEnv + "AUTH"
		}
		if s.Priv != "" && s.PrivEnv == "" && s.PrivFile == "" {
			s.PrivEnv = defaultEnv + "PRIV"
		}
	}
	return s
}

// liveWalk walks every subtree in roots from the agent at s.
func liveWalk(ctx context.Context, s snmp.Source, roots []string) (walk.Walk, error) {
	if err := (snmp.Manifest{Sources: []snmp.Source{s}}).Validate(); err != nil {
		return nil, err
	}
	g, err := snmp.Dial(ctx, s)
	if err != nil {
		return nil, err
	}
	defer g.Close()
	var all walk.Walk
	for _, r := range roots {
		w, err := snmp.WalkSubtree(ctx, g, r, s.MaxRepetitions)
		if err != nil {
			return nil, err
		}
		all = append(all, w...)
	}
	all.Sort()
	// Overlapping roots (a --oid under a default subtree) would duplicate.
	dedup := all[:0]
	for i, v := range all {
		if i > 0 && v.OID == all[i-1].OID {
			continue
		}
		dedup = append(dedup, v)
	}
	return dedup, nil
}

// ── import ───────────────────────────────────────────────────────────────

func runSnmpImport(args []string) int {
	fs := flag.NewFlagSet("snmp import", flag.ContinueOnError)
	tag := fs.String("tag", "", "device prefix")
	host := fs.String("host", "", "address the driver will poll")
	walkPath := fs.String("walk", "", "a recorded walk instead of the live agent")
	profile := fs.String("profile", "", "force a profile")
	ports := fs.String("ports", "", `interfaces by ifIndex: "1-24,26" or "all"`)
	outDir := fs.String("out", ".", "output directory")
	tagsOut := fs.String("tags-out", "tags/snmp.yaml", "tag file, relative to --out")
	tagsSkip := fs.String("tags-skip", "", "comma-separated globs to leave out of the tag file")
	plan := fs.Bool("plan", false, "print the requests one poll would send")
	lf := addLiveFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tag == "" || *host == "" {
		fmt.Fprintln(os.Stderr, "naut snmp import: --tag and --host are required")
		return 2
	}

	var w walk.Walk
	if *walkPath != "" {
		raw, err := os.ReadFile(*walkPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp import:", err)
			return 1
		}
		if w, err = walk.ParseBytes(raw); err != nil {
			fmt.Fprintf(os.Stderr, "naut snmp import: %s: %v\n", *walkPath, err)
			return 1
		}
	} else {
		src := lf.source(*tag, *host, "SNMP_"+strings.ToUpper(*tag)+"_")
		var err error
		if w, err = liveWalk(context.Background(), src, profiles.Subtrees()); err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp import:", err)
			return 1
		}
	}

	out, err := codegen.Generate(w, codegen.Options{
		Tag: *tag, Host: *host, Port: *lf.port, Profile: *profile, Ports: *ports,
		Version: *lf.version, User: *lf.user, Auth: *lf.auth, Priv: *lf.priv, Context: *lf.ctx,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp import:", err)
		return 1
	}

	// The header records what shapes the output and nothing about where the
	// walk came from, so the live import and the import of its recording
	// are the same bytes.
	command := "naut snmp import --tag " + *tag + " --host " + *host
	if *lf.port != snmp.DefaultPort {
		command += fmt.Sprintf(" --port %d", *lf.port)
	}
	if *profile != "" {
		command += " --profile " + *profile
	}
	if *ports != "" {
		command += " --ports " + *ports
	}
	if *lf.version == snmp.V3 {
		command += " --version 3 --user " + *lf.user
		if *lf.auth != "" {
			command += " --auth " + *lf.auth
		}
		if *lf.priv != "" {
			command += " --priv " + *lf.priv
		}
		if *lf.ctx != "" {
			command += " --context " + *lf.ctx
		}
	}

	if *plan {
		d, err := snmp.New(out.Manifest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp import:", err)
			return 1
		}
		fmt.Print(d.Plan())
	}
	tagsYAML, err := codegen.TagsYAML(out.Manifest, splitPatterns(*tagsSkip))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp import:", err)
		return 1
	}
	types, err := codegen.TypesST()
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp import:", err)
		return 1
	}
	manifestPath := filepath.Join(*outDir, "snmp_manifest.yaml")
	tagsPath := filepath.Join(*outDir, *tagsOut)
	typesPath := filepath.Join(*outDir, "hw_types.st")
	for _, f := range []struct {
		path string
		body []byte
	}{
		{manifestPath, codegen.ManifestYAML(out, command)},
		{tagsPath, tagsYAML},
		{typesPath, types},
	} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp import:", err)
			return 1
		}
		if err := os.WriteFile(f.path, f.body, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp import:", err)
			return 1
		}
	}
	fmt.Printf("profile %s: %d tag(s) for %s\n", out.Profile.Name, len(out.Manifest.Tags), *tag)
	if !out.Profile.Verified {
		fmt.Printf("  (profile %s is written from the published MIBs and not yet verified against hardware)\n", out.Profile.Name)
	}
	for _, n := range out.Notes {
		fmt.Printf("  note: %s\n", n)
	}
	fmt.Printf("wrote %s\n", manifestPath)
	fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", tagsPath, *tagsOut)
	fmt.Printf("wrote %s — the contract TYPEs; add it to the project's sources\n", typesPath)
	for _, s := range out.Manifest.Sources {
		for _, v := range []string{s.CommunityEnv, s.AuthEnv, s.PrivEnv} {
			if v != "" {
				fmt.Printf("the driver reads $%s where it runs\n", v)
			}
		}
	}
	return 0
}

// ── tags ─────────────────────────────────────────────────────────────────

func runSnmpTags(args []string) int {
	fs := flag.NewFlagSet("snmp tags", flag.ContinueOnError)
	out := fs.String("out", "tags/snmp.yaml", "output tag file")
	fs.StringVar(out, "o", "tags/snmp.yaml", "output tag file (alias for --out)")
	skip := fs.String("skip", "", "comma-separated globs to leave out, for tags declared by hand")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut snmp tags [-o tags/snmp.yaml] [--skip globs] <snmp_manifest.yaml>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp tags:", err)
		return 1
	}
	m, err := snmp.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut snmp tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	if err := m.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "naut snmp tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	body, err := codegen.TagsYAML(m, splitPatterns(*skip))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp tags:", err)
		return 1
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp tags:", err)
			return 1
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d struct tag(s)) — compose it with `tag-files: [%s]`\n", *out, len(m.Tags), *out)
	return 0
}

// ── browse ───────────────────────────────────────────────────────────────

func runSnmpBrowse(args []string) int {
	fs := flag.NewFlagSet("snmp browse", flag.ContinueOnError)
	host := fs.String("host", "", "agent address")
	var oids stringList
	fs.Var(&oids, "oid", "subtree to walk (repeatable; default: every subtree the profiles read)")
	record := fs.String("record", "", "write the walk to this file (snmpwalk -One format)")
	lf := addLiveFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "naut snmp browse: --host is required")
		return 2
	}
	roots := []string(oids)
	if len(roots) == 0 {
		roots = profiles.Subtrees()
	}
	src := lf.source("browse", *host, "SNMP_")
	w, err := liveWalk(context.Background(), src, roots)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp browse:", err)
		return 1
	}
	printWalk(os.Stdout, w)
	if len(w) == 0 {
		fmt.Fprintf(os.Stderr, "naut snmp browse: the agent answered but holds nothing under %s\n", strings.Join(roots, ", "))
	}
	if *record != "" {
		if err := os.WriteFile(*record, w.Bytes(), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp browse:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "recorded %d varbind(s) to %s — `naut snmp import --walk %s --tag … --host %s`\n",
			len(w), *record, *record, *host)
	}
	return 0
}

// printWalk renders the browse listing: the object name where a profile
// knows it, the value, its type.
func printWalk(out io.Writer, w walk.Walk) {
	for _, v := range w {
		fmt.Fprintf(out, "%s = %s (%s)\n", profiles.ObjectName(v.OID), v.ValueString(), v.Type)
	}
}

// ── serve ────────────────────────────────────────────────────────────────

func runSnmpServe(args []string) int {
	fs := flag.NewFlagSet("snmp serve", flag.ContinueOnError)
	walkPath := fs.String("walk", "", "the recording to serve")
	listen := fs.String("listen", "127.0.0.1:1161", "listen address")
	community := fs.String("community", "public", "community to answer to")
	ramp := fs.Bool("ramp", false, "move the 64-bit octet counters")
	from := fs.String("from", "", "plant controller URL to serve tags from")
	manifestPath := fs.String("manifest", "", "the monitoring project's snmp_manifest.yaml")
	sourceID := fs.String("source", "", "which manifest source this agent is")
	every := fs.Duration("every", time.Second, "plant poll period")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *walkPath == "" {
		fmt.Fprintln(os.Stderr, "naut snmp serve: --walk is required")
		return 2
	}
	var plant *servePlant
	if *from != "" || *manifestPath != "" {
		if *from == "" || *manifestPath == "" {
			fmt.Fprintln(os.Stderr, "naut snmp serve: --from and --manifest go together")
			return 2
		}
		p, err := loadServePlant(*manifestPath, *sourceID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp serve:", err)
			return 1
		}
		plant = p
		listenSet := false
		fs.Visit(func(f *flag.Flag) { listenSet = listenSet || f.Name == "listen" })
		if !listenSet {
			*listen = p.addr
		}
	}
	raw, err := os.ReadFile(*walkPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp serve:", err)
		return 1
	}
	w, err := walk.ParseBytes(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut snmp serve: %s: %v\n", *walkPath, err)
		return 1
	}
	a, stop, err := startSnmpServe(w, *listen, *community, *ramp, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp serve:", err)
		return 1
	}
	defer stop()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if plant != nil {
		pl, err := agent.NewPlant(a, plant.m, plant.source)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut snmp serve:", err)
			return 1
		}
		pl.Log = slog.New(slog.NewTextHandler(os.Stdout, nil))
		fmt.Printf("serving %d bound OID(s) of %s from %s (every %s)\n", len(pl.Feeds), plant.source, *from, *every)
		go pl.Run(ctx, agent.StateFetcher(*from, plant.m.TagPatterns(plant.source)), *every)
	}
	fmt.Printf("listening on %s (v2c, community %q) — ctrl-c to stop\n", a.Addr(), *community)
	<-ctx.Done()
	return 0
}

// servePlant is --manifest/--source resolved: the manifest and the source
// this agent stands in for.
type servePlant struct {
	m      snmp.Manifest
	source string
	addr   string
}

func loadServePlant(path, source string) (*servePlant, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := snmp.ParseManifest(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if source == "" {
		if len(m.Sources) != 1 {
			ids := make([]string, len(m.Sources))
			for i, s := range m.Sources {
				ids[i] = s.ID
			}
			return nil, fmt.Errorf("%s has %d sources (%s): name one with --source", path, len(m.Sources), strings.Join(ids, ", "))
		}
		source = m.Sources[0].ID
	}
	for _, s := range m.Sources {
		if s.ID == source {
			return &servePlant{m: m, source: source, addr: s.Addr()}, nil
		}
	}
	return nil, fmt.Errorf("%s: no source %q", path, source)
}

// startSnmpServe stands the agent up over a walk. Package-level so the test
// drives it in-process.
func startSnmpServe(w walk.Walk, listen, community string, ramp bool, out io.Writer) (*agent.Agent, func(), error) {
	if len(w) == 0 {
		return nil, nil, errors.New("the walk is empty")
	}
	a := agent.New(w, community)
	a.SetLogger(slog.New(slog.NewTextHandler(out, nil)))
	if ramp {
		// ifHCInOctets / ifHCOutOctets rows that are moving in the recording
		// (non-zero) move here too: 1–8 Mb/s, by row, so ports differ.
		n := 0
		for _, col := range []string{"1.3.6.1.2.1.31.1.1.1.6", "1.3.6.1.2.1.31.1.1.1.10"} {
			for i, v := range w.Subtree(col) {
				if v.Type == walk.Counter64 && v.Uint > 0 {
					_ = a.Ramp(v.OID, float64(125000*(1+i%8)))
					n++
				}
			}
		}
		fmt.Fprintf(out, "ramping %d octet counter(s)\n", n)
	}
	if err := a.Start(listen); err != nil {
		return nil, nil, err
	}
	name := "(no sysName)"
	if v, ok := w.Get("1.3.6.1.2.1.1.5.0"); ok {
		name = v.ValueString()
	}
	fmt.Fprintf(out, "serving %d varbind(s) of %s\n", len(w), name)
	return a, a.Stop, nil
}

// ── read ─────────────────────────────────────────────────────────────────

func runSnmpRead(args []string) int {
	fs := flag.NewFlagSet("snmp read", flag.ContinueOnError)
	walkPath := fs.String("walk", "", "the recording to read")
	manifestPath := fs.String("manifest", "", "the snmp_manifest.yaml to read it through")
	sourceID := fs.String("source", "", "which manifest source the walk is")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *walkPath == "" || *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "naut snmp read: --walk and --manifest are required")
		return 2
	}
	raw, err := os.ReadFile(*walkPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp read:", err)
		return 1
	}
	w, err := walk.ParseBytes(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut snmp read: %s: %v\n", *walkPath, err)
		return 1
	}
	p, err := loadServePlant(*manifestPath, *sourceID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp read:", err)
		return 1
	}
	tags, err := readSnmpOffline(w, p.m, p.source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut snmp read:", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(tags)
	return 0
}

// readSnmpOffline stands the walk up on a loopback agent, points ONE
// source of the manifest at it, and returns that source's tags after one
// complete poll — the same bytes the driver would deliver from the device.
func readSnmpOffline(w walk.Walk, m snmp.Manifest, source string) (map[string]any, error) {
	const env = "NAUT_SNMP_READ_COMMUNITY"
	a := agent.New(w, "public")
	a.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := a.Start("127.0.0.1:0"); err != nil {
		return nil, err
	}
	defer a.Stop()
	host, portS, _ := net.SplitHostPort(a.Addr())
	port, _ := strconv.Atoi(portS)
	one := snmp.Manifest{}
	for _, s := range m.Sources {
		if s.ID == source {
			s.Host, s.Port, s.Version, s.CommunityEnv, s.CommunityFile = host, port, snmp.V2c, env, ""
			s.User, s.Auth, s.AuthEnv, s.AuthFile, s.Priv, s.PrivEnv, s.PrivFile, s.Context = "", "", "", "", "", "", "", ""
			s.Enable, s.Interval = "", 200*time.Millisecond
			one.Sources = append(one.Sources, s)
		}
	}
	for _, t := range m.Tags {
		if t.Source == source {
			one.Tags = append(one.Tags, t)
		}
	}
	os.Setenv(env, "public")
	d, err := snmp.New(one, snmp.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	for {
		v, _ := d.ReadInputs()
		if n, _ := v[hw.LastPollTagName(source)].(int64); n > 0 {
			out := map[string]any{}
			for _, t := range one.Tags {
				if iv, ok := v[t.Name].(ir.Value); ok {
					out[t.Name] = runtime.Plain(iv)
				}
			}
			return out, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("source %s: no complete poll of the walk in 30s", source)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
