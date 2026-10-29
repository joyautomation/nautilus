package main

// `naut plcnext import` turns the local bus's device list into a
// plcnext_manifest.yaml (modules, offsets, every channel bound) and its
// tag file, so offsets are never typed by hand. The firmware does not
// hand naut the device types — BusConductor publishes only NUM_MODULES —
// so the list is a file; --check-bus, on the controller, compares its
// length with NUM_MODULES.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyautomation/nautilus/plcnext"
)

func runPlcnextImport(args []string) int {
	fs := plcnextFlags("import")
	outDir := fs.String("out", ".", "output directory")
	tagsOut := fs.String("tags-out", "tags/plcnext.yaml", "tag file to write, relative to --out")
	transport := fs.String("transport", plcnext.TransportGRPC, "the manifest's transport: grpc or shm")
	component := fs.String("bus-conductor", plcnext.DefaultComponent, "BusConductor component instance (none: no handshake)")
	checkBus := fs.Bool("check-bus", false, "on the controller: compare the list with BusConductor's NUM_MODULES")
	force := fs.Bool("force", false, "replace a manifest or tag file that holds something else")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut plcnext import [--out .] [--transport grpc|shm] [--check-bus] <plcnext_devices.yaml>")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "naut plcnext import:", err)
		return 1
	}
	if *transport != plcnext.TransportGRPC && *transport != plcnext.TransportShm {
		return fail(fmt.Errorf("--transport %q (want grpc or shm)", *transport))
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fail(err)
	}
	list, err := plcnext.ParseDeviceList(raw)
	if err != nil {
		return fail(fmt.Errorf("%s: %w", fs.Arg(0), err))
	}
	if *checkBus {
		c, ctx, cancel, ok := plcnextConn(fs, "import")
		if !ok {
			return 1
		}
		n, err := plcnext.BusModules(ctx, c, *component)
		c.Close()
		cancel()
		if err != nil {
			return fail(err)
		}
		if err := list.CheckCount(n); err != nil {
			return fail(err)
		}
		fmt.Printf("%s: NUM_MODULES=%d, the list's count\n", *component, n)
	}
	imp, err := plcnext.Import(list, plcnext.ImportOptions{BusConductor: *component, Transport: *transport})
	if err != nil {
		return fail(err)
	}
	command := plcnextImportCommand(fs.Arg(0), *transport, *component)
	manifestYAML := plcnext.ImportedYAML(imp, command)
	tagsYAML, err := plcnext.TagsYAML(imp.Manifest, *tagsOut)
	if err != nil {
		return fail(err)
	}
	manifestPath := filepath.Join(*outDir, "plcnext_manifest.yaml")
	tagsPath := filepath.Join(*outDir, *tagsOut)
	if err := writeImport([]importFile{{path: manifestPath, body: manifestYAML}, {path: tagsPath, body: tagsYAML}}, *force); err != nil {
		return fail(err)
	}
	var unknown []string
	for _, im := range imp.Modules {
		if !im.Known {
			unknown = append(unknown, fmt.Sprintf("slot %d (%s)", im.Slot, im.Part))
		}
	}
	fmt.Printf("%d module(s), %d byte(s) in each image\n", len(imp.Modules), lastByte(imp))
	fmt.Printf("wrote %s (%d tag bindings) — driver: {type: plcnext, manifest: plcnext_manifest.yaml}\n", manifestPath, len(imp.Manifest.Tags))
	fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", tagsPath, *tagsOut)
	if len(unknown) > 0 {
		fmt.Printf("not in naut's table, laid out but not bound: %s — bind their channels in the manifest (a commented example is there), then `naut plcnext tags`\n", strings.Join(unknown, ", "))
	}
	return 0
}

// lastByte is how far the modules reach, the same in both images.
func lastByte(imp plcnext.Imported) int {
	n := 0
	for _, im := range imp.Modules {
		n = im.Offset + im.In
	}
	return n
}

func plcnextImportCommand(path, transport, component string) string {
	cmd := "naut plcnext import " + filepath.Base(path)
	if transport != plcnext.TransportGRPC {
		cmd += " --transport " + transport
	}
	if component != plcnext.DefaultComponent {
		cmd += " --bus-conductor " + component
	}
	return cmd
}
