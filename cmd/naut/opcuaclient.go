package main

// `naut opcua` is the commissioning side of the OPC UA CLIENT driver
// (`driver: {type: opcua}`) — not the opcua: section, which serves this
// project's own tags and needs no tooling. Subcommands: import
// (browse a server → opcua_manifest.yaml + tags/opcua.yaml), browse (print
// what a server exposes), and tags (re-derive the tag file from a committed
// manifest, no server needed). It mirrors `naut eip import|browse|tags`
// and `naut modbus …` deliberately: one importer per protocol, one shape to
// learn. The password for a UserName login is never a flag — set
// NAUTILUS_OPCUA_PASSWORD, the rule the sparkplug tools keep for MQTT.

import (
	"context"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/opcua/codegen"
)

const opcuaUsage = `naut opcua — OPC UA tools

Usage:
  naut opcua import --endpoint opc.tcp://host:4840 [flags]
        Browse a server's address space and generate opcua_manifest.yaml +
        tags/opcua.yaml. Byte-identical on re-run against an unchanged server.
  naut opcua browse --endpoint opc.tcp://host:4840 [flags]
        Print every variable under --root: node id, type, access, name.
  naut opcua tags <opcua_manifest.yaml> [flags]
        Re-derive the tag file from an already-committed manifest — no
        server needed (-o path, --name for the __Online companion).

Reads only. Nothing these commands do writes to the server.

Connection flags (import and browse):
  --endpoint   Server URL (required), e.g. opc.tcp://10.0.0.5:62541/discovery
  --security   Policy: None | Basic256Sha256 | … (default: strongest offered)
  --mode       None | Sign | SignAndEncrypt (default: strongest of the policy)
  --user       UserName token; password from NAUTILUS_OPCUA_PASSWORD
  --pki-dir    Client certificate directory (default ~/.nautilus/opcua)
  --timeout    Dial and request timeout (default 10s)
  --root       Node to browse from (default i=85, the Objects folder)
  --depth      Levels below root to descend (default 0 = unlimited)

Import flags:
  --name       Driver name; the companion tag is <name>__Online (default:
               the opcua driver in --out's nautilus.yaml whose endpoint:
               matches, else "opcua")
  --prefix     Prepended to every tag name
  --tags       Comma-separated glob patterns selecting generated tags
  --writable   Comma-separated globs marking (server-writable) tags writable.
               Default none: every binding is read-only until you opt in
  --out        Output directory (default ".")
  --tags-out   Tag file to emit, relative to --out (default tags/opcua.yaml)
  --tags-skip  Comma-separated globs to leave OUT of the tag file, for tags
               the project declares by hand
`

func runOPCUA(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, opcuaUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runOPCUAImport(args[1:])
	case "browse":
		return runOPCUABrowse(args[1:])
	case "tags":
		return runOPCUATags(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut opcua: unknown subcommand %q\n\n%s", args[0], opcuaUsage)
		return 2
	}
}

// opcuaConn holds the connection flags shared by import and browse.
type opcuaConn struct {
	endpoint, security, mode, user, pkiDir, root string
	timeout                                      time.Duration
	depth                                        int
}

func (c *opcuaConn) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.endpoint, "endpoint", "", "server URL (opc.tcp://host:4840)")
	fs.StringVar(&c.security, "security", "", "security policy (default: strongest offered)")
	fs.StringVar(&c.mode, "mode", "", "message security mode: None, Sign, SignAndEncrypt")
	fs.StringVar(&c.user, "user", "", "UserName token (password from NAUTILUS_OPCUA_PASSWORD)")
	fs.StringVar(&c.pkiDir, "pki-dir", "", "client certificate directory (default ~/.nautilus/opcua)")
	fs.DurationVar(&c.timeout, "timeout", 10*time.Second, "dial and request timeout")
	fs.StringVar(&c.root, "root", "", "node to browse from (default i=85, Objects)")
	fs.IntVar(&c.depth, "depth", 0, "levels below root to descend (0 = unlimited)")
}

// dial connects with the flags, resolving the default pki dir.
func (c *opcuaConn) dial(cmd string) (*opcua.Session, *ua.NodeID, error) {
	if c.endpoint == "" {
		return nil, nil, fmt.Errorf("--endpoint is required")
	}
	pki := c.pkiDir
	if pki == "" {
		if home, err := os.UserHomeDir(); err == nil {
			pki = filepath.Join(home, ".nautilus", "opcua")
		}
	}
	root := opcua.ObjectsFolder
	if c.root != "" {
		id, err := ua.ParseNodeID(c.root)
		if err != nil {
			return nil, nil, fmt.Errorf("--root %q: %w", c.root, err)
		}
		root = id
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	sess, err := opcua.Dial(ctx, c.endpoint, opcua.Connect{
		Security:    opcua.Security{Policy: c.security, Mode: c.mode},
		Username:    c.user,
		Password:    os.Getenv("NAUTILUS_OPCUA_PASSWORD"),
		PKIDir:      pki,
		Timeout:     c.timeout,
		SessionName: "naut opcua " + cmd,
	})
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(os.Stderr, "connected to %s (%s)\n", c.endpoint, sess.Endpoint.SecurityPolicyURI[strings.LastIndex(sess.Endpoint.SecurityPolicyURI, "#")+1:])
	return sess, root, nil
}

func (c *opcuaConn) browse(cmd string) (opcua.BrowseResult, error) {
	sess, root, err := c.dial(cmd)
	if err != nil {
		return opcua.BrowseResult{}, err
	}
	defer sess.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	fmt.Fprintf(os.Stderr, "browsing from %s...\n", root)
	return opcua.Browse(ctx, sess.Client, root, c.depth, func(msg string) { fmt.Fprintln(os.Stderr, "  "+msg) })
}

// ── import ───────────────────────────────────────────────────────────────

func runOPCUAImport(args []string) int {
	fs := flag.NewFlagSet("opcua import", flag.ContinueOnError)
	var conn opcuaConn
	conn.flags(fs)
	name := fs.String("name", "", "driver name; the companion tag is <name>__Online (default: from nautilus.yaml, else opcua)")
	prefix := fs.String("prefix", "", "prepended to every tag name")
	tags := fs.String("tags", "", "comma-separated glob patterns selecting generated tags")
	writable := fs.String("writable", "", "comma-separated globs marking tags writable")
	outDir := fs.String("out", ".", "output directory")
	tagsOut := fs.String("tags-out", "tags/opcua.yaml", "tag file to emit, relative to --out")
	tagsSkip := fs.String("tags-skip", "", "comma-separated globs to leave OUT of the tag file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// The companion is named before dialing, so a project that cannot
	// tell which driver this is fails fast.
	online, how, err := opcuaOnlineTag(*name, os.DirFS(*outDir), conn.endpoint, "opcua_manifest.yaml")
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua import:", err)
		return 2
	}
	br, err := conn.browse("import")
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua import:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "found %d variables under %d nodes\n", len(br.Variables), br.Visited)

	out, err := codegen.Generate(br, codegen.Options{
		Patterns:         splitPatterns(*tags),
		WritablePatterns: splitPatterns(*writable),
		Prefix:           *prefix,
		Endpoint:         conn.endpoint,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua import:", err)
		return 1
	}
	for _, s := range out.Skipped {
		fmt.Fprintln(os.Stderr, "  skipped:", s)
	}

	command := "naut opcua import --endpoint " + conn.endpoint
	if conn.root != "" {
		command += " --root " + conn.root
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua import:", err)
		return 1
	}
	manifestPath := filepath.Join(*outDir, "opcua_manifest.yaml")
	raw, err := codegen.ManifestYAML(out.Manifest, command)
	if err == nil {
		err = os.WriteFile(manifestPath, raw, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua import:", err)
		return 1
	}
	tagsPath := filepath.Join(*outDir, *tagsOut)
	if err := writeOPCUATagsFile(tagsPath, out.Manifest, command, online, splitPatterns(*tagsSkip)); err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua import:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d tag bindings) and %s — compose it with `tag-files: [%s]`\n",
		manifestPath, len(out.Manifest.Tags), tagsPath, *tagsOut)
	fmt.Printf("online tag: %s (%s)\n", online, how)
	return 0
}

// opcuaOnlineTag is the <driver name>__Online companion a generated tag
// file carries: from --name when given, else from the project's
// nautilus.yaml (project.OPCUAOnlineTag, which names drivers exactly as the
// loader does).
func opcuaOnlineTag(name string, projectDir iofs.FS, endpoint, manifest string) (online, how string, err error) {
	if name != "" {
		return project.OPCUAOnlineTagFor(name), "--name " + name, nil
	}
	return project.OPCUAOnlineTag(projectDir, endpoint, manifest)
}

func writeOPCUATagsFile(path string, m opcua.Manifest, command, onlineTag string, skip []string) error {
	raw, err := codegen.TagsYAML(m, command, onlineTag, skip)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, raw, 0o644)
}

// ── tags ─────────────────────────────────────────────────────────────────

// runOPCUATags re-derives the tag file from an ALREADY COMMITTED manifest:
// during review, in CI, or after the driver name changes — without a server.
func runOPCUATags(args []string) int {
	fs := flag.NewFlagSet("opcua tags", flag.ContinueOnError)
	out := fs.String("o", "tags/opcua.yaml", "output tag file")
	name := fs.String("name", "", "driver name; the companion tag is <name>__Online (default: from ./nautilus.yaml, else opcua)")
	skip := fs.String("skip", "", "comma-separated globs to leave OUT, for tags the project declares by hand")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut opcua tags [-o tags/opcua.yaml] [--name opcua] <opcua_manifest.yaml>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua tags:", err)
		return 1
	}
	m, err := opcua.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut opcua tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	// The project is the working directory; its driver is found by the
	// manifest given.
	online, _, err := opcuaOnlineTag(*name, os.DirFS("."), "", filepath.ToSlash(fs.Arg(0)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua tags:", err)
		return 2
	}
	if err := writeOPCUATagsFile(*out, m, "naut opcua tags "+fs.Arg(0), online, splitPatterns(*skip)); err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d tag bindings) — compose it with `tag-files: [%s]`\n", *out, len(m.Tags), *out)
	return 0
}

// ── browse ───────────────────────────────────────────────────────────────

func runOPCUABrowse(args []string) int {
	fs := flag.NewFlagSet("opcua browse", flag.ContinueOnError)
	var conn opcuaConn
	conn.flags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	br, err := conn.browse("browse")
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut opcua browse:", err)
		return 1
	}
	fmt.Printf("namespaces:\n")
	for i, ns := range br.Namespaces {
		fmt.Printf("  ns=%d  %s\n", i, ns)
	}
	vars := append([]opcua.Variable(nil), br.Variables...)
	sort.Slice(vars, func(i, j int) bool { return strings.Join(vars[i].Path, "/") < strings.Join(vars[j].Path, "/") })
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tNODE ID\tTYPE\tACCESS\tDESCRIPTION")
	for _, v := range vars {
		access := ""
		if v.Readable {
			access += "r"
		}
		if v.Writable {
			access += "w"
		}
		typ := v.Type
		if !v.Scalar {
			typ += " (not scalar)"
		} else if v.ValueRank >= 0 {
			typ += "[]"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", strings.Join(v.Path, "/"), v.NodeID, typ, access, v.Description)
	}
	w.Flush()
	fmt.Fprintf(os.Stderr, "%d variables under %d nodes\n", len(br.Variables), br.Visited)
	return 0
}
