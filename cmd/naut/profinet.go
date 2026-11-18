package main

// `naut profinet` is the offline side of nautilus as a PROFINET IO
// device: gsdml (the file the PLC's engineering tool imports, from the
// same profinet_device.yaml the driver serves) and tags (the tag file).
// Neither touches the network.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joyautomation/nautilus/profinet"
)

const profinetUsage = `naut profinet — PROFINET IO device tools (offline)

Usage:
  naut profinet gsdml [-o dir] [--date YYYYMMDD] <profinet_device.yaml>
        Write the GSDML file for the device the manifest describes, to
        import into the PLC's engineering tool (TIA Portal: Options >
        Manage general station description files). Same manifest, same
        layout: the PLC is configured with exactly what the driver serves.
  naut profinet tags [-o tags/profinet.yaml] [--online name] <profinet_device.yaml>
        Write the tag file: the PLC's outputs as input tags, its inputs as
        output tags, each with its IEC type, plus the online companion.
`

func runProfinet(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, profinetUsage)
		return 2
	}
	switch args[0] {
	case "gsdml":
		return runProfinetGSDML(args[1:])
	case "tags":
		return runProfinetTags(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut profinet: unknown subcommand %q\n\n%s", args[0], profinetUsage)
		return 2
	}
}

func loadDeviceManifest(verb, path string) (profinet.DeviceManifest, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut profinet %s: %v\n", verb, err)
		return profinet.DeviceManifest{}, false
	}
	m, err := profinet.ParseDeviceManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut profinet %s: %s: %v\n", verb, path, err)
		return profinet.DeviceManifest{}, false
	}
	return m, true
}

func runProfinetGSDML(args []string) int {
	fs := flag.NewFlagSet("profinet gsdml", flag.ContinueOnError)
	dir := fs.String("o", ".", "output directory")
	date := fs.String("date", "", "the date in the file name, YYYYMMDD (default today)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut profinet gsdml [-o dir] [--date YYYYMMDD] <profinet_device.yaml>")
		return 2
	}
	m, ok := loadDeviceManifest("gsdml", fs.Arg(0))
	if !ok {
		return 1
	}
	when := time.Now()
	if *date != "" {
		t, err := time.Parse("20060102", *date)
		if err != nil {
			fmt.Fprintf(os.Stderr, "naut profinet gsdml: --date %q: want YYYYMMDD\n", *date)
			return 2
		}
		when = t
	}
	f, err := profinetGSDML(m, when)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut profinet gsdml:", err)
		return 1
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "naut profinet gsdml:", err)
		return 1
	}
	out := filepath.Join(*dir, f.Name)
	if err := os.WriteFile(out, f.Body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut profinet gsdml:", err)
		return 1
	}
	fmt.Printf("wrote %s: station %s, %d subslot(s); import it into the PLC's engineering tool\n", out, m.Station, len(m.Subslots()))
	return 0
}

func runProfinetTags(args []string) int {
	fs := flag.NewFlagSet("profinet tags", flag.ContinueOnError)
	out := fs.String("o", "tags/profinet.yaml", "output tag file")
	online := fs.String("online", "profinet_device__Online", "the online companion tag (the driver's name + __Online)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut profinet tags [-o tags/profinet.yaml] [--online name] <profinet_device.yaml>")
		return 2
	}
	m, ok := loadDeviceManifest("tags", fs.Arg(0))
	if !ok {
		return 1
	}
	body, err := profinet.DeviceTagsYAML(m, *online, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut profinet tags:", err)
		return 1
	}
	if d := filepath.Dir(*out); d != "." {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut profinet tags:", err)
			return 1
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut profinet tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d tags): compose it with `tag-files: [%s]`\n", *out, len(m.Tags)+1, *out)
	return 0
}
