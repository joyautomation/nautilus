package main

// naut camera discover (#724 slice 4c): list the ONVIF cameras on the local network.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/camera"
)

// discoverFn is camera.Discover; a test replaces it.
var discoverFn = camera.Discover

func runCameraDiscover(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("camera discover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ifc := fs.String("interface", "", "probe one network interface (default: every one that is up and multicast)")
	timeout := fs.Duration("timeout", 3*time.Second, "how long to wait for answers")
	asJSON := fs.Bool("json", false, "print the answers as JSON")
	fs.Usage = func() { fmt.Fprint(stderr, cameraUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "naut camera discover: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *timeout <= 0 || *timeout > time.Minute {
		fmt.Fprintln(stderr, "naut camera discover: --timeout is between 1ms and 1m")
		return 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	found, err := discoverFn(ctx, camera.DiscoverOptions{Timeout: *timeout, Interface: *ifc})
	if err != nil {
		fmt.Fprintf(stderr, "naut camera discover: %v\n", err)
		return 1
	}
	if *asJSON {
		if found == nil {
			found = []camera.Found{}
		}
		b, _ := json.MarshalIndent(found, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	if len(found) == 0 {
		fmt.Fprintf(stdout, "No ONVIF cameras answered in %s. Run this on a machine on the cameras' network (the probe is multicast and does not cross routers), and check that the cameras have ONVIF discovery on.\n", *timeout)
		return 0
	}
	fmt.Fprintf(stdout, "%d ONVIF camera(s) answered:\n\n", len(found))
	for i, f := range found {
		name := f.Name
		if name == "" {
			name = "(no name)"
		}
		fmt.Fprintf(stdout, "%s  %s\n", name, strings.Join(nonBlank(f.Hardware, "from "+f.From), "  "))
		for _, a := range f.XAddrs {
			fmt.Fprintf(stdout, "    %s\n", a)
		}
		if f.Elsewhere {
			fmt.Fprintf(stdout, "    note: an address above is not on %s, where the answer came from: a camera behind NAT or a bridge, or something pretending to be one. Look before you trust it.\n", f.From)
		}
		fmt.Fprintf(stdout, "    manifest row:\n      - { id: cam%d, label: %q, asset: <equipment id>, kind: onvif, url: %q, credentials: true }\n\n", i+1, name, f.XAddrs[0])
	}
	fmt.Fprintln(stdout, "Then: naut alarms secrets set camera.<id>   (\"user:password\" on stdin)")
	return 0
}

func nonBlank(s ...string) []string {
	var out []string
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
