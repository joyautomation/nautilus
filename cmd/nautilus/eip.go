package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/eip/codegen"
	"github.com/joyautomation/nautilus/eip/logix"
)

const eipUsage = `nautilus eip — EtherNet/IP tools

Usage:
  nautilus eip import --host <ip> [flags]   Browse a Logix controller and
                                            generate eip_types.st + eip_manifest.go.
  nautilus eip browse --host <ip> [flags]   Print the controller's tag list.

Import flags:
  --host       Controller IP or hostname (required)
  --slot       Processor backplane slot (default 0)
  --port       EtherNet/IP TCP port (default 44818)
  --tags       Comma-separated glob patterns selecting device tags
               (default: all user tags; module I/O needs explicit patterns)
  --writable   Comma-separated glob patterns for tags the program writes back
  --out        Output directory (default ".")
  --package    Go package for the manifest file (default "main")
`

func runEIP(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, eipUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runEIPImport(args[1:])
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

	stPath := filepath.Join(*outDir, "eip_types.st")
	goPath := filepath.Join(*outDir, "eip_manifest.go")
	if err := os.WriteFile(stPath, []byte(out.TypesST), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
		return 1
	}
	if err := os.WriteFile(goPath, []byte(out.ManifestGo), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus eip import:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d types) and %s (%d tag bindings)\n",
		stPath, len(out.Manifest.Types), goPath, len(out.Manifest.Tags))
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
