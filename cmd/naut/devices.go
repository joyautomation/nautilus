package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/virtdev"
)

const devicesUsage = `naut devices — a project's field devices, by profile, and their virtual twins

Usage:
  naut devices add <profile> [project-dir] [--from dir]
                                 copy a profile (and its simulator) into the
                                 project's profiles/ folder: the project's pinned
                                 copy, which devices: rows refer to
  naut devices render [dir]      print what nautilus.yaml's devices: derive at load:
                                 the struct types, the tags, the driver bindings
  naut devices up [dir]          start every virtual device (below) on its wire
                                 face and run the simulators and process model
                                 until ctrl-c
  naut devices check [dir]       load the virtual devices, their profiles, simulators and
                                 process model, and compile the plant; no sockets
  naut devices profiles          list the built-in device profiles
  naut devices show <profile>    print a profile's points (a built-in id, or a
                                 path to a project's own .yaml)

A project declares its devices in nautilus.yaml:

  devices:
    - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.11 }

CONV is then one struct tag of the profile's type (CONV.StatusWord reads the
drive back) plus a command tag per writable point (CONV_ControlWord). See
docs/design/device-library.md.

A virtual device speaks the real device's protocol and register map, built
from public documentation. Every devices: row in nautilus.yaml is one,
simulating the project's vendored profile. virtual-devices.yaml adds the rest:

  devices:
    - name: CONV                   # a devices: row: only listen and init here
      init: { Actual1: 0 }
    - name: RB1                    # IEC identifier; its points are RB1_<Point>
      profile: asi-275m      # built-in id, or profiles/mine.yaml
      endpoint: 192.168.10.21      # what the project's driver dials
      listen: 127.0.0.1:15021      # wire face (default: an ephemeral port)
      init: { Input3: true }       # power-up values, simulator hooks
  model:
    program: plant.st              # the process model, an ST PROGRAM

See docs/design/virtual-devices.md.
`

func runDevices(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, devicesUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return runDevicesAdd(args[1:], os.Stdout)
	case "render":
		return runDevicesRender(args[1:], os.Stdout)
	case "up":
		return runDevicesUp(args[1:])
	case "check":
		return runDevicesCheck(args[1:])
	case "profiles":
		return runDevicesProfiles(args[1:], os.Stdout)
	case "show":
		return runDevicesShow(args[1:], os.Stdout)
	case "help", "-h", "--help":
		fmt.Print(devicesUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "naut devices: unknown subcommand %q\n\n%s", args[0], devicesUsage)
		return 2
	}
}

// devicesDir parses the optional [dir] argument.
func devicesDir(name string, args []string) (string, bool) {
	fs := flag.NewFlagSet("devices "+name, flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return "", false
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	return dir, true
}

func loadDeviceSet(cmd, dir string) (*virtdev.Set, bool) {
	set, err := loadVirtualDevices(dir, "")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "naut devices %s: no virtual devices in %s (no devices: in %s, no %s)\n", cmd, dir, project.ManifestName, virtdev.FileName)
		} else {
			fmt.Fprintf(os.Stderr, "naut devices %s: %v\n", cmd, err)
		}
		return nil, false
	}
	return set, true
}

func runDevicesUp(args []string) int {
	dir, ok := devicesDir("up", args)
	if !ok {
		return 2
	}
	set, ok := loadDeviceSet("up", dir)
	if !ok {
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run, err := virtdev.Start(ctx, set, virtdev.StartOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut devices up:", err)
		return 1
	}
	defer run.Stop()
	printEndpoints(os.Stdout, run.Endpoints)
	fmt.Println("ctrl-c to stop")
	<-ctx.Done()
	return 0
}

func printEndpoints(w io.Writer, eps []virtdev.Endpoint) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tPROFILE\tLISTEN\tUNIT\tSTANDS IN FOR")
	for _, e := range eps {
		stands := e.Endpoint
		if stands == "" {
			stands = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", e.Device, e.Profile, e.Listen, e.Unit, stands)
	}
	tw.Flush()
}

func runDevicesCheck(args []string) int {
	dir, ok := devicesDir("check", args)
	if !ok {
		return 2
	}
	set, ok := loadDeviceSet("check", dir)
	if !ok {
		return 1
	}
	plant, err := virtdev.NewPlant(set)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut devices check:", err)
		return 1
	}
	if _, err := runtime.New(plant.Options()); err != nil {
		fmt.Fprintf(os.Stderr, "naut devices check: the plant does not compile: %v\n\ngenerated main program:\n%s", err, plant.Program())
		return 1
	}
	fmt.Printf("%d device(s) ok\n", len(set.Devices))
	return 0
}

func runDevicesProfiles(args []string, w io.Writer) int {
	if len(args) > 0 {
		fmt.Fprint(os.Stderr, devicesUsage)
		return 2
	}
	ps, err := devprofile.Builtin()
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut devices profiles:", err)
		return 1
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROFILE\tPROTOCOL\tPOINTS\tTITLE")
	for _, p := range ps {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", p.ID, p.Protocol, len(p.Points), p.Title)
	}
	tw.Flush()
	return 0
}

func runDevicesShow(args []string, w io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, devicesUsage)
		return 2
	}
	wd, _ := os.Getwd()
	p, err := devprofile.ResolveProfile(wd, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut devices show:", err)
		return 1
	}
	fmt.Fprintf(w, "%s — %s\n", p.ID, p.Title)
	if p.Maker != "" || p.Model != "" {
		fmt.Fprintf(w, "maker: %s  model: %s\n", p.Maker, p.Model)
	}
	if o := p.OPCUA; o != nil {
		port := o.Port
		if port == 0 {
			port = 4840
		}
		fmt.Fprintf(w, "opcua: port %d, security None, anonymous\n", port)
		for i, uri := range o.Namespaces {
			fmt.Fprintf(w, "  ns=%d %s\n", i+1, uri)
		}
		for k, v := range p.Params {
			fmt.Fprintf(w, "param {%s} default %q\n", k, v)
		}
	}
	if m := p.Modbus; m != nil && p.Protocol == devprofile.ProtoModbus {
		port := m.Port
		if port == 0 {
			port = 502
		}
		order := "big"
		if m.WordOrder != "" {
			order = m.WordOrder
		}
		fmt.Fprintf(w, "modbus: port %d, unit %d, word order %s\n", port, m.UnitID, order)
	}
	for _, d := range p.Docs {
		fmt.Fprintf(w, "doc %s: %s", d.ID, d.Title)
		if d.Code != "" {
			fmt.Fprintf(w, " (%s)", d.Code)
		}
		if d.URL != "" {
			fmt.Fprintf(w, " %s", d.URL)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "POINT\tTYPE\tACCESS\tADDRESS / NODE ID\tFORMAT\tSOURCE\tDESCRIPTION")
	for _, pt := range p.Points {
		access := "read"
		if pt.Writable {
			access = "write"
		}
		addr := fmt.Sprintf("%s %d", pt.Table, pt.Address)
		if pt.NodeID != "" {
			addr = pt.NodeID
		}
		src := pt.Doc
		if pt.Unverified {
			src = "UNVERIFIED"
		}
		format := pt.Format
		if pt.Scale != 0 && pt.Scale != 1 {
			format += fmt.Sprintf(" ×%g", pt.Scale)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", pt.Name, pt.Type, access, addr, format, src, pt.Desc)
	}
	tw.Flush()
	if p.Simulator != nil {
		fmt.Fprintf(w, "\nsimulator: %s (%s)\n", p.Simulator.Block, filepath.Base(p.Simulator.Source))
	}
	if n := strings.TrimSpace(p.Notes); n != "" {
		fmt.Fprintf(w, "\n%s\n", n)
	}
	return 0
}
