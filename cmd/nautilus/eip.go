package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/eip/codegen"
	"github.com/joyautomation/nautilus/eip/logix"
	"gopkg.in/yaml.v3"
)

const eipUsage = `nautilus eip — EtherNet/IP tools

Usage:
  nautilus eip import --host <ip> [flags]   Browse a Logix controller and
                                            generate eip_types.st + eip_manifest.go.
  nautilus eip browse --host <ip> [flags]   Print the controller's tag list.
  nautilus eip tags <eip_manifest.yaml>    Re-derive the tag file from an
                                           already-committed manifest — no
                                           controller needed (-o path).

Import flags:
  --host       Controller IP or hostname (required)
  --slot       Processor backplane slot (default 0)
  --port       EtherNet/IP TCP port (default 44818)
  --tags       Comma-separated glob patterns selecting device tags
               (default: all user tags; module I/O needs explicit patterns)
  --writable   Comma-separated glob patterns for tags the program writes back
  --out        Output directory (default ".")
  --package    Go package for the manifest file (default "main")
  --format     Manifest format: "go" (eip_manifest.go, library projects)
               or "yaml" (eip_manifest.yaml, nautilus.yaml projects)
`

func runEIP(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, eipUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runEIPImport(args[1:])
	case "tags":
		return runEIPTags(args[1:])
	case "browse":
		return runEIPBrowse(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "nautilus eip: unknown subcommand %q\n\n%s", args[0], eipUsage)
		return 2
	}
}

func eipDial(host string, slot, port int) (*logix.Controller, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	opts := []logix.Option{logix.WithSlot(slot)}
	if port != 0 {
		opts = append(opts, logix.WithPort(port))
	}
	return logix.Dial(ctx, host, opts...)
}

func splitPatterns(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runEIPImport(args []string) int {
	fs := flag.NewFlagSet("eip import", flag.ContinueOnError)
	host := fs.String("host", "", "controller IP or hostname")
	slot := fs.Int("slot", 0, "processor backplane slot")
	port := fs.Int("port", 0, "EtherNet/IP TCP port")
	tags := fs.String("tags", "", "comma-separated tag glob patterns")
	writable := fs.String("writable", "", "comma-separated writable tag glob patterns")
	outDir := fs.String("out", ".", "output directory")
	pkg := fs.String("package", "main", "Go package for the manifest")
	format := fs.String("format", "go", "manifest format: go (eip_manifest.go, for library projects) or yaml (eip_manifest.yaml, for nautilus.yaml manifest projects)")
	tagsOut := fs.String("tags-out", "tags/eip.yaml", "tag file to emit (--format yaml only); compose it with tag-files:")
	tagsSkip := fs.String("tags-skip", "", "comma-separated globs to leave OUT of the tag file, for tags the manifest declares by hand")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "nautilus eip import: --host is required")
		return 2
	}

	ctrl, err := eipDial(*host, *slot, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
		return 1
	}
	defer ctrl.Close()

	fmt.Fprintf(os.Stderr, "browsing %s (slot %d)...\n", *host, *slot)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	br, err := ctrl.Browse(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip import: browse:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "found %d tags, %d templates, %d programs\n",
		len(br.Symbols), len(br.Templates), len(br.Programs))

	out, err := codegen.Generate(br, codegen.Options{
		Patterns:         splitPatterns(*tags),
		WritablePatterns: splitPatterns(*writable),
		Package:          *pkg,
		Host:             *host,
		Slot:             *slot,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
		return 1
	}
	for _, s := range out.Skipped {
		fmt.Fprintln(os.Stderr, "  skipped:", s)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
		return 1
	}

	stPath := filepath.Join(*outDir, "eip_types.st")
	if err := os.WriteFile(stPath, []byte(out.TypesST), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
		return 1
	}
	var manifestPath string
	switch *format {
	case "go":
		manifestPath = filepath.Join(*outDir, "eip_manifest.go")
		if err := os.WriteFile(manifestPath, []byte(out.ManifestGo), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
			return 1
		}
	case "yaml":
		// The manifest as data — what a nautilus.yaml project's
		// `driver: {type: eip, manifest: eip_manifest.yaml}` consumes.
		raw, err := yaml.Marshal(out.Manifest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
			return 1
		}
		header := fmt.Sprintf("# Generated by `nautilus eip import --host %s --format yaml` — the\n# controller's UDT shapes + tag bindings. Re-run the import to refresh;\n# validated against the live controller at driver startup.\n", *host)
		manifestPath = filepath.Join(*outDir, "eip_manifest.yaml")
		if err := os.WriteFile(manifestPath, append([]byte(header), raw...), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "nautilus eip import: --format must be go or yaml")
		return 2
	}
	// The tags file only makes sense for a manifest project; a Go project
	// composes []runtime.TagDef in code.
	tagsPath := ""
	if *format == "yaml" {
		tagsPath = filepath.Join(*outDir, *tagsOut)
		if err := writeTagsFile(tagsPath, out.Manifest, *host, splitPatterns(*tagsSkip)); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
			return 1
		}
	}
	fmt.Printf("wrote %s (%d types) and %s (%d tag bindings)\n",
		stPath, len(out.Manifest.Types), manifestPath, len(out.Manifest.Tags))
	if tagsPath != "" {
		fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", tagsPath, *tagsOut)
	}
	return 0
}

// writeTagsFile renders the manifest's bindings as a nautilus tag file,
// creating the directory the path implies (tags/ by convention).
func writeTagsFile(path string, m eip.Manifest, host string, skip []string) error {
	raw, err := codegen.TagsYAML(m, host, skip)
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

// runEIPTags re-derives the tag file from an ALREADY COMMITTED eip manifest.
// The import needs a controller on the network; this needs only the repo, so
// a tag file can be regenerated during review, in CI, or after the scan
// classes in the manifest change — without a PLC.
func runEIPTags(args []string) int {
	fs := flag.NewFlagSet("eip tags", flag.ContinueOnError)
	out := fs.String("o", "tags/eip.yaml", "output tag file")
	host := fs.String("host", "the controller", "host to name in the generated header")
	skip := fs.String("skip", "", "comma-separated globs to leave OUT, for tags the manifest declares by hand")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: nautilus eip tags [-o tags/eip.yaml] <eip_manifest.yaml>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip tags:", err)
		return 1
	}
	var m eip.Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		fmt.Fprintf(os.Stderr, "nautilus eip tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	if err := writeTagsFile(*out, m, *host, splitPatterns(*skip)); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d tag bindings) — compose it with `tag-files: [%s]`\n",
		*out, len(m.Tags), *out)
	return 0
}

func runEIPBrowse(args []string) int {
	fs := flag.NewFlagSet("eip browse", flag.ContinueOnError)
	host := fs.String("host", "", "controller IP or hostname")
	slot := fs.Int("slot", 0, "processor backplane slot")
	port := fs.Int("port", 0, "EtherNet/IP TCP port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "nautilus eip browse: --host is required")
		return 2
	}
	ctrl, err := eipDial(*host, *slot, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip browse:", err)
		return 1
	}
	defer ctrl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	br, err := ctrl.Browse(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip browse:", err)
		return 1
	}
	for _, s := range br.Symbols {
		typeName := ""
		if s.IsStruct() {
			if t, ok := br.Templates[s.TemplateID()]; ok {
				typeName = t.Name
			} else {
				typeName = fmt.Sprintf("template:0x%x", s.TemplateID())
			}
		} else if t, ok := logix.TypeByCode(s.ElementaryCode()); ok {
			typeName = t.Name
		} else {
			typeName = fmt.Sprintf("0x%04x", s.Type)
		}
		dims := ""
		if n := s.DimCount(); n > 0 {
			dims = fmt.Sprintf("[%d]", s.Dims[0])
			for d := 1; d < n; d++ {
				dims += fmt.Sprintf("[%d]", s.Dims[d])
			}
		}
		fmt.Printf("%-50s %s%s\n", s.Name, typeName, dims)
	}
	return 0
}
