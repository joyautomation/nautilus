package main

// `nautilus sparkplug` is the import side of the Sparkplug B host
// application: it listens to a group, asks the sites it hears from to birth
// again, and writes the three committed files a central project consumes —
// sparkplug_types.st, sparkplug_manifest.yaml and tags/sparkplug.yaml.
//
// It mirrors `nautilus eip import|browse|tags` deliberately, down to the flag
// names: one importer per protocol, one shape to learn. The only structural
// difference is --sites, which generates the same files from a committed site
// list instead of a live broker — sixty edge projects that already know their
// tag lists should not have to be simultaneously online for the central
// project to build.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/host"
	sphostgen "github.com/joyautomation/nautilus/sparkplug/host/codegen"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

const sparkplugUsage = `nautilus sparkplug — Sparkplug B host application tools

Usage:
  nautilus sparkplug import --broker <url> --group <id> [flags]
        Listen to a Sparkplug group, ask its edge nodes to birth, and generate
        sparkplug_types.st + sparkplug_manifest.yaml + tags/sparkplug.yaml.
  nautilus sparkplug import --sites <file> [flags]
        Generate the same files from a committed site list — no broker.
  nautilus sparkplug browse --broker <url> --group <id> [flags]
        Print the nodes, devices, metrics and template types discovered.
  nautilus sparkplug tags <sparkplug_manifest.yaml> [flags]
        Re-derive the tag file from an already-committed manifest — no broker
        needed (--out path, --skip globs).

Import flags:
  --broker      Broker URL, tcp://host:1883 or ssl://host:8883 (required
                unless --sites)
  --group       Sparkplug group_id; repeatable, or comma-separated
  --host-id     MQTT client id base for the throwaway import client
                (default "nautilus-import"). Nothing retained is published
                under it — an importer is not a primary host.
  --listen      How long to listen for births (default 30s). Discovery
                returns as soon as every node it heard from has birthed and
                nothing new has arrived for 2s, so a healthy fleet costs a
                second or two, not the whole window.
  --rebirth     Send NCMD Node Control/Rebirth to every node heard from that
                has not birthed (default true). Retained births are forbidden
                by the spec, so a mid-stream import sees nothing until it asks.
  --nodes       Comma-separated globs selecting edge_node_ids
  --metrics     Comma-separated globs selecting metrics, matched against the
                metric name ("Well/Level") and its device path ("PLC1/Pump/Run")
  --writable    Same shape of globs, marking the matching tags writable —
                their values go back out as NCMD/DCMD
  --sites       Generate from a site list instead of a broker (see below)
  --layout      flat (default). struct is not yet supported.
  --prefix      node (default: tag names start with the edge_node_id), none,
                or a literal prefix (single-node imports only)
  --out         Output directory (default ".")
  --tags-out    Tag file to emit, relative to --out (default tags/sparkplug.yaml)
  --tags-skip   Comma-separated globs to leave OUT of the tag file, for tags
                the project declares by hand. A pattern that matches nothing
                is an error.

Globs are path.Match patterns: "*" does not cross "/", so a folder-shaped
metric name needs "Pump/*" rather than "Pump*".

--sites file (offline generation):
  group: PomonaWRD
  types:
    - name: Motor
      fields:
        - {name: Speed, type: Double}
        - {name: Run,   type: Boolean}
  sites:
    - node: W6                 # the Sparkplug edge_node_id
      prefix: W6               # optional; default sanitize(node)
      metrics:                 # node-level metrics (NBIRTH/NDATA)
        - {name: Well/Level, type: Double}
        - {name: Pump1,      type: Motor}     # a template instance
      devices:
        - device: PLC1
          metrics:
            - {name: Pump/Run,     type: Boolean}
            - {name: Pump/SpeedSP, type: Double, writable: true, init: 0.0}

  type: is a Sparkplug datatype name (Boolean, Int8..UInt64, DateTime, Float,
  Double, String, Text, UUID) or one of the types: entries. Keys are the
  lowercased Go field names and unknown keys are an error, exactly like
  sparkplug_manifest.yaml.
`

func runSparkplug(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, sparkplugUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runSparkplugImport(args[1:])
	case "browse":
		return runSparkplugBrowse(args[1:])
	case "tags":
		return runSparkplugTags(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "nautilus sparkplug: unknown subcommand %q\n\n%s", args[0], sparkplugUsage)
		return 2
	}
}

// stringList is a repeatable string flag that also accepts comma-separated
// values, so `--group A --group B` and `--group A,B` agree.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

// ── import ───────────────────────────────────────────────────────────────

func runSparkplugImport(args []string) int {
	fs := flag.NewFlagSet("sparkplug import", flag.ContinueOnError)
	var groups stringList
	broker := fs.String("broker", "", "broker URL (tcp://host:1883)")
	fs.Var(&groups, "group", "Sparkplug group_id (repeatable, or comma-separated)")
	hostID := fs.String("host-id", "nautilus-import", "client id base for the import client")
	listen := fs.Duration("listen", 30*time.Second, "how long to listen for births (stops early once every node heard from has birthed)")
	rebirth := fs.Bool("rebirth", true, "ask nodes heard from to birth again")
	username := fs.String("username", "", "MQTT username (password from NAUTILUS_MQTT_PASSWORD)")
	nodes := fs.String("nodes", "", "comma-separated globs selecting edge nodes")
	metrics := fs.String("metrics", "", "comma-separated globs selecting metrics")
	writable := fs.String("writable", "", "comma-separated globs marking metrics writable")
	sites := fs.String("sites", "", "generate from a site list instead of a broker")
	layout := fs.String("layout", "flat", "tag layout: flat (struct is not yet supported)")
	prefix := fs.String("prefix", "node", "tag prefix: node, none, or a literal")
	outDir := fs.String("out", ".", "output directory")
	tagsOut := fs.String("tags-out", "tags/sparkplug.yaml", "tag file to emit, relative to --out")
	tagsSkip := fs.String("tags-skip", "", "comma-separated globs to leave OUT of the tag file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *broker == "" && *sites == "" {
		fmt.Fprintln(os.Stderr, "nautilus sparkplug import: --broker or --sites is required")
		return 2
	}

	var skipped []string
	opts := sphostgen.Options{
		Nodes:    splitPatterns(*nodes),
		Metrics:  splitPatterns(*metrics),
		Writable: splitPatterns(*writable),
		Layout:   *layout,
		Prefix:   *prefix,
		OnSkip:   func(s string) { skipped = append(skipped, s) },
	}

	var m host.Manifest
	var err error
	if *sites != "" {
		m, err = manifestFromSites(*sites, opts)
	} else {
		m, err = manifestFromBroker(*broker, *username, *hostID, groups, *listen, *rebirth, opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sparkplug import:", err)
		return 1
	}
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, "  skipped:", s)
	}

	paths, err := writeSparkplugFiles(*outDir, *tagsOut, m, splitPatterns(*tagsSkip))
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sparkplug import:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d types) and %s (%d nodes, %d bindings)\n",
		paths.types, len(m.Types), paths.manifest, len(m.Nodes), len(m.Tags))
	fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", paths.tags, *tagsOut)
	return 0
}

// manifestFromSites is the offline path: no broker, just a committed list.
func manifestFromSites(path string, opts sphostgen.Options) (host.Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return host.Manifest{}, err
	}
	specs, err := sphostgen.ParseSites(raw)
	if err != nil {
		return host.Manifest{}, fmt.Errorf("%s: %w", path, err)
	}
	return sphostgen.FromSites(specs, opts)
}

// manifestFromBroker listens to the group and builds the manifest from what
// birthed.
func manifestFromBroker(broker, username, hostID string, groups stringList,
	listen time.Duration, rebirth bool, opts sphostgen.Options) (host.Manifest, error) {

	births, err := discoverBirths(broker, username, hostID, groups, listen, rebirth)
	if err != nil {
		return host.Manifest{}, err
	}
	if len(births) == 0 {
		return host.Manifest{}, fmt.Errorf(
			"no births heard on %s in %s — check --group, raise --listen, or generate "+
				"offline with --sites", broker, listen)
	}
	nodes, devices, metrics := countBirths(births)
	fmt.Fprintf(os.Stderr, "heard %d node(s), %d device(s), %d metric(s)\n", nodes, devices, metrics)
	return sphostgen.FromBirths(births, opts)
}

// discoverBirths runs one import-time listen against the broker.
func discoverBirths(broker, username, hostID string, groups stringList,
	listen time.Duration, rebirth bool) ([]host.Birth, error) {

	ctx, cancel := context.WithTimeout(context.Background(), listen+30*time.Second)
	defer cancel()
	return host.Discover(ctx, host.DiscoverOptions{
		BrokerURL: broker,
		ClientID:  fmt.Sprintf("%s-%d", hostID, os.Getpid()),
		Username:  username,
		Password:  os.Getenv("NAUTILUS_MQTT_PASSWORD"),
		Groups:    groups,
		Listen:    listen,
		Quiet:     2 * time.Second,
		Rebirth:   rebirth,
	})
}

func countBirths(births []host.Birth) (nodes, devices, metrics int) {
	seen := map[string]bool{}
	for _, b := range births {
		if b.IsNode() {
			if !seen[b.Group+"/"+b.EdgeNode] {
				seen[b.Group+"/"+b.EdgeNode] = true
				nodes++
			}
		} else {
			devices++
		}
		metrics += len(b.Payload.Metrics)
	}
	return nodes, devices, metrics
}

// generated is where the three files landed.
type generated struct{ types, manifest, tags string }

// writeSparkplugFiles renders and writes the file set. Wholesale overwrite,
// deterministic output — a regeneration diffs cleanly against the last one.
func writeSparkplugFiles(outDir, tagsOut string, m host.Manifest, skip []string) (generated, error) {
	var g generated
	typesST, err := sphostgen.TypesST(m)
	if err != nil {
		return g, err
	}
	manifestYAML, err := sphostgen.ManifestYAML(m)
	if err != nil {
		return g, err
	}
	tagsYAML, err := sphostgen.TagsYAML(m, skip)
	if err != nil {
		return g, err
	}

	// sparkplug_types.st has to land in the project ROOT: project.go treats
	// every root-level .st without a PROGRAM as a library, which is how the
	// generated TYPEs become visible to `type:` references.
	g.types = filepath.Join(outDir, "sparkplug_types.st")
	if err := os.WriteFile(g.types, typesST, 0o644); err != nil {
		return g, err
	}
	g.manifest = filepath.Join(outDir, "sparkplug_manifest.yaml")
	if err := os.WriteFile(g.manifest, manifestYAML, 0o644); err != nil {
		return g, err
	}
	g.tags = filepath.Join(outDir, tagsOut)
	if dir := filepath.Dir(g.tags); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return g, err
		}
	}
	return g, os.WriteFile(g.tags, tagsYAML, 0o644)
}

// ── browse ───────────────────────────────────────────────────────────────

func runSparkplugBrowse(args []string) int {
	fs := flag.NewFlagSet("sparkplug browse", flag.ContinueOnError)
	var groups stringList
	broker := fs.String("broker", "", "broker URL (tcp://host:1883)")
	fs.Var(&groups, "group", "Sparkplug group_id (repeatable, or comma-separated)")
	hostID := fs.String("host-id", "nautilus-import", "client id base for the browse client")
	listen := fs.Duration("listen", 30*time.Second, "how long to listen for births")
	rebirth := fs.Bool("rebirth", true, "ask nodes heard from to birth again")
	username := fs.String("username", "", "MQTT username (password from NAUTILUS_MQTT_PASSWORD)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *broker == "" {
		fmt.Fprintln(os.Stderr, "nautilus sparkplug browse: --broker is required")
		return 2
	}
	births, err := discoverBirths(*broker, *username, *hostID, groups, *listen, *rebirth)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sparkplug browse:", err)
		return 1
	}
	if len(births) == 0 {
		fmt.Fprintf(os.Stderr, "no births heard on %s in %s\n", *broker, *listen)
		return 1
	}
	printBirths(births)
	return 0
}

// printBirths renders the discovery the way `nautilus eip browse` renders a
// controller's tag list: one line per metric, with the type it carries.
func printBirths(births []host.Birth) {
	for _, b := range births {
		who := b.Group + "/" + b.EdgeNode
		kind := "NBIRTH"
		if !b.IsNode() {
			who += "/" + b.Device
			kind = "DBIRTH"
		}
		fmt.Printf("%s %s (%d metrics)\n", kind, who, len(b.Payload.Metrics))

		var defs, data []string
		for _, m := range b.Payload.Metrics {
			t, ok := m.Value.(*sparkplug.Template)
			if ok && t != nil && t.IsDefinition {
				defs = append(defs, fmt.Sprintf("  type %-40s %d members", m.Name, len(t.Metrics)))
				continue
			}
			typ := m.Datatype.String()
			if m.Datatype == spb.DataType_Template && ok && t != nil {
				typ = "Template " + t.TemplateRef
			}
			data = append(data, fmt.Sprintf("  %-42s %s", m.Name, typ))
		}
		sort.Strings(defs)
		sort.Strings(data)
		for _, l := range defs {
			fmt.Println(l)
		}
		for _, l := range data {
			fmt.Println(l)
		}
	}
}

// ── tags ─────────────────────────────────────────────────────────────────

// runSparkplugTags re-derives the tag file from an ALREADY COMMITTED manifest.
// The import needs the sites on the wire; this needs only the repo, so the tag
// file can be regenerated during review, in CI, or after the manifest is
// hand-edited — with every site offline.
func runSparkplugTags(args []string) int {
	fs := flag.NewFlagSet("sparkplug tags", flag.ContinueOnError)
	out := fs.String("out", "tags/sparkplug.yaml", "output tag file")
	fs.StringVar(out, "o", "tags/sparkplug.yaml", "output tag file (alias for --out)")
	skip := fs.String("skip", "", "comma-separated globs to leave OUT, for tags declared by hand")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: nautilus sparkplug tags [--out tags/sparkplug.yaml] <sparkplug_manifest.yaml>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sparkplug tags:", err)
		return 1
	}
	m, err := host.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nautilus sparkplug tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	body, err := sphostgen.TagsYAML(m, splitPatterns(*skip))
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sparkplug tags:", err)
		return 1
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus sparkplug tags:", err)
			return 1
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sparkplug tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d bindings + %d nodes' companion tags) — compose it with `tag-files: [%s]`\n",
		*out, len(m.Tags), len(m.Nodes), *out)
	return 0
}
