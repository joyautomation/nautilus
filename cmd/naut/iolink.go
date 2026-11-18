package main

// naut iolink import | show: IODDs for the devices on IO-Link masters'
// ports (docs/design/iolink.md §8).

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/iolink/iodd"
)

// ioddDir is the project folder vendored IODDs live in, as profiles/ is
// for device profiles: a port's iodd: is a plain file name in it.
const ioddDir = "iodd"

const iolinkUsage = `usage: naut iolink <cmd>

  import <iodd.xml> [project-dir]   check an IODD and copy it into the
                                    project's iodd/ folder (the copy is the
                                    pin: import never replaces it)
  show <iodd.xml>                   print what an IODD says: identity,
                                    process data items and their IEC types,
                                    variables
`

func runIOLink(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, iolinkUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runIOLinkImport(args[1:], os.Stdout)
	case "show":
		return runIOLinkShow(args[1:], os.Stdout)
	case "help", "-h", "--help":
		fmt.Print(iolinkUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "naut iolink: unknown subcommand %q\n\n%s", args[0], iolinkUsage)
		return 2
	}
}

func runIOLinkImport(args []string, w io.Writer) int {
	fl := flag.NewFlagSet("iolink import", flag.ContinueOnError)
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if fl.NArg() < 1 || fl.NArg() > 2 {
		fmt.Fprint(os.Stderr, "usage: naut iolink import <iodd.xml> [project-dir]\n")
		return 2
	}
	dir := "."
	if fl.NArg() == 2 {
		dir = fl.Arg(1)
	}
	if err := importIODD(dir, fl.Arg(0), w); err != nil {
		fmt.Fprintln(os.Stderr, "naut iolink import:", err)
		return 1
	}
	return 0
}

// importIODD vendors src into dir/iodd/ under its own file name, after
// checking that it parses and that at least one of its process data
// layouts can be decoded. It writes with devices add's writer: iodd/ must
// be the project's own folder, the file is created O_EXCL, and a copy
// already there is never replaced.
func importIODD(dir, src string, w io.Writer) error {
	name := filepath.Base(src)
	if !devprofile.IsFileName(name) || !strings.EqualFold(filepath.Ext(name), ".xml") {
		return fmt.Errorf("%q: an IODD is a .xml file", src)
	}
	if _, err := os.Stat(filepath.Join(dir, project.ManifestName)); err != nil {
		return fmt.Errorf("%s is not a project (no %s)", dir, project.ManifestName)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	d, err := iodd.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	notes, err := decodable(d)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	dst := filepath.Join(dir, ioddDir)
	if fi, err := os.Lstat(dst); err == nil && !fi.IsDir() {
		return fmt.Errorf("%s/ is not a folder (a symlink?): import writes only into the project's own %s/", ioddDir, ioddDir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	same, err := existingCopy(dst, name, data)
	if err != nil {
		return fmt.Errorf("%w: a copy in %s/ is the project's pin, and import never replaces it (compare the two and replace the file yourself)", err, ioddDir)
	}
	if same {
		fmt.Fprintf(w, "%s/%s is already there, unchanged\n", ioddDir, name)
	} else {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if err := createInDir(dst, name, data); err != nil {
			return err
		}
		fmt.Fprintf(w, "added %s/%s\n", ioddDir, name)
	}
	for _, n := range notes {
		fmt.Fprintln(w, "note:", n)
	}
	id := d.Identity
	fmt.Fprintf(w, "%s %s: vendor id %d, device id %d (IODD %s)\n", id.VendorName, id.DeviceName, id.VendorID, id.DeviceID, d.Version)
	fmt.Fprintf(w, "put it on a master's port in %s:\n  ports:\n    <n>: { name: <Name>, iodd: %s }\n", project.ManifestName, name)
	return nil
}

// decodable refuses an IODD none of whose process data layouts the codec
// can decode, and notes the layouts it cannot (a device with a variant
// nautilus does not decode can still run in one it does).
func decodable(d *iodd.IODD) (notes []string, err error) {
	if len(d.ProcessData) == 0 {
		return nil, fmt.Errorf("the IODD declares no process data")
	}
	ok := 0
	var first error
	for _, pd := range d.ProcessData {
		var bad error
		for _, im := range []*iodd.Image{pd.In, pd.Out} {
			if im == nil {
				continue
			}
			if err := im.Check(); err != nil && bad == nil {
				bad = fmt.Errorf("process data %s: %w", pd.ID, err)
			}
		}
		if bad == nil {
			ok++
			continue
		}
		if first == nil {
			first = bad
		}
		notes = append(notes, bad.Error()+"; that layout cannot be used")
	}
	if ok == 0 {
		return nil, first
	}
	return notes, nil
}

func runIOLinkShow(args []string, w io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, "usage: naut iolink show <iodd.xml>\n")
		return 2
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut iolink show:", err)
		return 1
	}
	d, err := iodd.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut iolink show: %s: %v\n", filepath.Base(args[0]), err)
		return 1
	}
	showIODD(d, w)
	return 0
}

func showIODD(d *iodd.IODD, w io.Writer) {
	id := d.Identity
	fmt.Fprintf(w, "%s %s (IODD %s)\n", id.VendorName, id.DeviceName, d.Version)
	fmt.Fprintf(w, "vendor id %d (0x%04X), device id %d (0x%06X)", id.VendorID, id.VendorID, id.DeviceID, id.DeviceID)
	for _, a := range id.AdditionalDeviceIDs {
		fmt.Fprintf(w, ", also %d", a)
	}
	fmt.Fprintln(w)
	for _, v := range id.Variants {
		fmt.Fprintf(w, "  variant %s: %s\n", v.ProductID, v.Description)
	}
	for _, pd := range d.ProcessData {
		fmt.Fprintf(w, "\nprocess data %s", pd.ID)
		if c := pd.Condition; c != nil {
			fmt.Fprintf(w, " (pd-variant %d: when %s = %d)", c.Value, c.VariableID, c.Value)
		}
		fmt.Fprintln(w)
		for _, im := range []struct {
			dir string
			im  *iodd.Image
		}{{"in", pd.In}, {"out", pd.Out}} {
			if im.im == nil {
				continue
			}
			octets := "octets"
			if im.im.Octets() == 1 {
				octets = "octet"
			}
			fmt.Fprintf(w, "  %s, %d bits (%d %s)", im.dir, im.im.BitLength, im.im.Octets(), octets)
			if err := im.im.Check(); err != nil {
				fmt.Fprintf(w, ": not decoded: %v", err)
			}
			fmt.Fprintln(w)
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "    SUB\tBITS\tNAME\tIODD\tIEC\tSCALING")
			for _, it := range im.im.Items {
				hi := it.BitOffset + it.Type.BitLength - 1
				scale := ""
				if it.Scaled {
					scale = fmt.Sprintf("× %g + %g", it.Gradient, it.Offset)
				}
				if it.UnitCode != 0 {
					scale = strings.TrimSpace(scale + fmt.Sprintf(" unit %d", it.UnitCode))
				}
				iec := it.IECType()
				if iec == "" {
					iec = "-"
				}
				fmt.Fprintf(tw, "    %d\t%d..%d\t%s\t%s\t%s\t%s\n", it.Subindex, hi, it.BitOffset, it.Name, it.Type.Name, iec, scale)
			}
			tw.Flush()
		}
	}
	fmt.Fprintln(w, "\nvariables")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  INDEX\tACCESS\tID\tNAME\tTYPE\tDEFAULT")
	for _, v := range d.Variables {
		idx := fmt.Sprint(v.Index)
		if v.Index < 0 {
			idx = "?"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", idx, v.Access, v.ID, v.Name, v.Type.Name, v.Default)
	}
	tw.Flush()
}
