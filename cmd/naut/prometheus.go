package main

// `naut prometheus` is the commissioning side of the Prometheus scrape
// driver: import (a live or recorded `/metrics` body → prometheus_manifest.yaml
// + tags/prometheus.yaml + hw_types.st, via the "node" profile), browse (a
// live poke, and the commissioning tool that records the fixture import/
// serve/the foreign test all consume), serve (stand in for the exporter
// from a recorded body), and tags (re-derive the tag file, no scrape
// needed). It mirrors `naut modbus import|browse|serve|tags` deliberately —
// one importer per protocol, one shape to learn (docs/design/it-drivers.md
// §8).

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/prom"
	"github.com/joyautomation/nautilus/prom/codegen"
	"github.com/joyautomation/nautilus/prom/serve"
)

const prometheusUsage = `naut prometheus — Prometheus / node_exporter tools

Usage:
  naut prometheus import --url <url> [--file metrics.txt] --tag ID [flags]
        Live or offline: scrape --url (or, with --file, read a recorded body
        instead — --url still names the exporter it came from) and expand
        the "node" profile into explicit bindings → prometheus_manifest.yaml
        + tags/prometheus.yaml + hw_types.st. Byte-identical on re-run, and
        the same bytes from the live exporter and from a fixture recorded
        from it (--url is what makes that true even with --file).
  naut prometheus browse --url <url> [--record file]
        Live poke: fetch and print every sample, grouped by metric — the
        commissioning tool. --record writes the raw body import/serve/the
        foreign test consume.
  naut prometheus serve --file metrics.txt [--listen 127.0.0.1:9100] [--ramp]
        Stand in for the exporter from its recording, for naut run on a
        laptop and for the driver's own tests.
  naut prometheus tags <prometheus_manifest.yaml> [--out tags/prometheus.yaml]
        Regenerate the tag file only from a committed manifest.

Import flags:
  --url        The source's scrape URL (required; e.g. http://127.0.0.1:9100/metrics) —
               scraped live unless --file is also given
  --file       A recorded body (from --record) to read instead of scraping --url
  --tag        Source id AND tag prefix (required — "NODE1")
  --profile    Metric-to-member profile (default, and only: node)
  --out        Output directory (default ".")
  --tags-out   Tag file to emit, relative to --out (default tags/prometheus.yaml)
  --force      Replace a manifest or tag file that holds something else
               (a hand edit, another device's import); refused otherwise
  --timeout    Live scrape timeout (default 5s)

Browse flags:
  --url        Scrape URL (required)
  --record     Write the raw response body to this file
  --timeout    Request timeout (default 5s)

Serve flags:
  --file       Recorded body to replay (required)
  --listen     Listen address (default 127.0.0.1:9100)
  --ramp       Drift every counter-typed series continuously, for a bench
               that looks alive with no exporter behind it
`

func runPrometheus(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, prometheusUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runPrometheusImport(args[1:])
	case "browse":
		return runPrometheusBrowse(args[1:])
	case "serve":
		return runPrometheusServe(args[1:])
	case "tags":
		return runPrometheusTags(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut prometheus: unknown subcommand %q\n\n%s", args[0], prometheusUsage)
		return 2
	}
}

// ── import ───────────────────────────────────────────────────────────────

func runPrometheusImport(args []string) int {
	fs := flag.NewFlagSet("prometheus import", flag.ContinueOnError)
	url := fs.String("url", "", "live scrape URL")
	file := fs.String("file", "", "a recorded body instead of a live scrape")
	tag := fs.String("tag", "", "source id and tag prefix (required)")
	profile := fs.String("profile", "node", "metric-to-member profile")
	outDir := fs.String("out", ".", "output directory")
	force := fs.Bool("force", false, "replace a manifest or tag file that holds something else (a hand edit, another device's import)")
	tagsOut := fs.String("tags-out", "tags/prometheus.yaml", "tag file to emit, relative to --out")
	timeout := fs.Duration("timeout", 5*time.Second, "live scrape timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tag == "" {
		fmt.Fprintln(os.Stderr, "naut prometheus import: --tag is required")
		return 2
	}
	if *url == "" {
		fmt.Fprintln(os.Stderr, "naut prometheus import: --url is required (the manifest's source URL — with --file it names the exporter the recording came from, without --file it is also what gets scraped)")
		return 2
	}

	// --url is ALWAYS the manifest's recorded source URL — the thing that
	// makes "the same bytes from a live device and from its recording" true
	// (docs/design/it-drivers.md §8) is that a --file import can still
	// name the real exporter the fixture came from. --file only swaps
	// where the BODY comes from.
	var body []byte
	if *file != "" {
		raw, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut prometheus import:", err)
			return 1
		}
		body = raw
	} else {
		raw, err := scrapeOnce(*url, *timeout)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut prometheus import:", err)
			return 1
		}
		body = raw
	}
	sc, err := prom.ParseText(body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus import:", err)
		return 1
	}

	out, err := codegen.Generate(sc, codegen.Options{Tag: *tag, URL: *url, Profile: *profile})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus import:", err)
		return 1
	}
	if missing := codegen.ReportMissing(sc, out.Manifest); len(missing) > 0 {
		fmt.Fprintln(os.Stderr, "naut prometheus import: bound metrics absent from the scrape:")
		for _, m := range missing {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
	}

	command := fmt.Sprintf("naut prometheus import --tag %s --url %s", *tag, *url)
	if *file != "" {
		command += " --file " + filepath.Base(*file)
	}
	if *profile != "node" {
		command += " --profile " + *profile
	}

	manifestPath := filepath.Join(*outDir, "prometheus_manifest.yaml")
	tagsYAML, err := codegen.TagsYAML(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus import:", err)
		return 1
	}
	tagsPath := filepath.Join(*outDir, *tagsOut)
	typesPath := filepath.Join(*outDir, "hw_types.st")
	types, err := hw.TypesST("prometheus")
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus import:", err)
		return 1
	}
	if err := writeImport([]importFile{
		{path: manifestPath, body: codegen.ManifestYAML(out.Manifest, command)},
		{path: tagsPath, body: tagsYAML},
		{path: typesPath, body: types, generated: true},
	}, *force); err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus import:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d tag bindings)\n", manifestPath, len(out.Manifest.Tags))
	fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", tagsPath, *tagsOut)
	fmt.Printf("wrote %s\n", typesPath)
	return 0
}

// scrapeOnce is import/browse's live path — a plain GET, no auth (a
// project's manifest carries credentials; the commissioning tool is run by
// a person who is either already on the exporter's network or has none to
// give).
func scrapeOnce(url string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", url, err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return buf, nil
}

// ── tags ─────────────────────────────────────────────────────────────────

func runPrometheusTags(args []string) int {
	fs := flag.NewFlagSet("prometheus tags", flag.ContinueOnError)
	out := fs.String("out", "tags/prometheus.yaml", "output tag file")
	fs.StringVar(out, "o", "tags/prometheus.yaml", "output tag file (alias for --out)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut prometheus tags [-o tags/prometheus.yaml] <prometheus_manifest.yaml>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus tags:", err)
		return 1
	}
	m, err := prom.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut prometheus tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	body, err := codegen.TagsYAML(codegen.Output{Manifest: m, Desc: map[string]string{}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus tags:", err)
		return 1
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut prometheus tags:", err)
			return 1
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d tag bindings) — compose it with `tag-files: [%s]`\n", *out, len(m.Tags), *out)
	return 0
}

// ── browse ───────────────────────────────────────────────────────────────

func runPrometheusBrowse(args []string) int {
	fs := flag.NewFlagSet("prometheus browse", flag.ContinueOnError)
	url := fs.String("url", "", "scrape URL (required)")
	record := fs.String("record", "", "write the raw response body to this file")
	timeout := fs.Duration("timeout", 5*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *url == "" {
		fmt.Fprintln(os.Stderr, "naut prometheus browse: --url is required")
		return 2
	}
	body, err := scrapeOnce(*url, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus browse:", err)
		return 1
	}
	if *record != "" {
		if err := os.WriteFile(*record, body, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "naut prometheus browse:", err)
			return 1
		}
		fmt.Printf("recorded %s (%d bytes)\n", *record, len(body))
	}
	sc, err := prom.ParseText(body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus browse:", err)
		return 1
	}
	printScrape(os.Stdout, sc)
	return 0
}

func printScrape(w *os.File, sc prom.Scrape) {
	names := make([]string, 0, len(sc.Families)+8)
	seen := map[string]bool{}
	for _, s := range sc.Samples {
		if !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	byName := map[string][]prom.Sample{}
	for _, s := range sc.Samples {
		byName[s.Name] = append(byName[s.Name], s)
	}
	for _, name := range names {
		fam := sc.Families[name]
		if fam.Type != "" {
			fmt.Fprintf(w, "%s (%s)\n", name, fam.Type)
		} else {
			fmt.Fprintf(w, "%s\n", name)
		}
		samples := byName[name]
		for i, s := range samples {
			if i >= 5 {
				fmt.Fprintf(w, "  ... %d more\n", len(samples)-5)
				break
			}
			fmt.Fprintf(w, "  %s = %v\n", labelString(s.Labels), s.Value)
		}
	}
}

func labelString(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%q", k, labels[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// ── serve ────────────────────────────────────────────────────────────────

func runPrometheusServe(args []string) int {
	fs := flag.NewFlagSet("prometheus serve", flag.ContinueOnError)
	file := fs.String("file", "", "recorded body to replay (required)")
	listen := fs.String("listen", "127.0.0.1:9100", "listen address")
	ramp := fs.Bool("ramp", false, "drift every counter-typed series continuously")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "naut prometheus serve: --file is required")
		return 2
	}
	body, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus serve:", err)
		return 1
	}
	srv, err := serve.New(body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus serve:", err)
		return 1
	}
	if err := srv.Start(*listen); err != nil {
		fmt.Fprintln(os.Stderr, "naut prometheus serve:", err)
		return 1
	}
	if *ramp {
		srv.Ramp(time.Second, 1)
	}
	defer srv.Stop()
	fmt.Printf("serving %s — ctrl-c to stop\n", srv.URL())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	return 0
}
