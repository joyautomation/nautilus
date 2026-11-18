package main

// naut devices add | render: a project's devices: declared by profile
// (docs/design/device-library.md).

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/project"
)

// runDevicesAdd vendors a profile into a project: profiles/<id>.yaml and
// its simulator, copied from the built-in library or from --from dir (a
// team's own profiles, a folder under git). The copy is the project's pin.
func runDevicesAdd(args []string, w io.Writer) int {
	fl := flag.NewFlagSet("devices add", flag.ContinueOnError)
	from := fl.String("from", "", "copy from this folder of profiles instead of the built-in library")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if fl.NArg() < 1 || fl.NArg() > 2 {
		fmt.Fprint(os.Stderr, "usage: naut devices add [--from dir] <profile> [project-dir]\n")
		return 2
	}
	id, dir := fl.Arg(0), "."
	if fl.NArg() == 2 {
		dir = fl.Arg(1)
	}
	if err := addProfile(dir, id, *from, w); err != nil {
		fmt.Fprintln(os.Stderr, "naut devices add:", err)
		return 1
	}
	return 0
}

func addProfile(dir, id, from string, w io.Writer) error {
	if _, err := os.Stat(filepath.Join(dir, project.ManifestName)); err != nil {
		return fmt.Errorf("%s is not a project (no %s)", dir, project.ManifestName)
	}
	// Read and check the source before copying anything.
	read := func(name string) ([]byte, error) {
		if from == "" {
			return devprofile.BuiltinFile(name)
		}
		return os.ReadFile(filepath.Join(from, name))
	}
	raw, err := read(id + ".yaml")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if from == "" {
				return fmt.Errorf("no built-in profile %q (naut devices profiles lists them)", id)
			}
			return fmt.Errorf("no profile %s.yaml in %s", id, from)
		}
		return err
	}
	p, err := devprofile.ParseProfile(raw, id+".yaml")
	if err != nil {
		return err
	}
	if p.ID != id {
		return fmt.Errorf("%s.yaml declares profile %q", id, p.ID)
	}
	files := map[string][]byte{id + ".yaml": raw}
	if s := p.Simulator; s != nil {
		src, err := read(s.Source)
		if err != nil {
			return fmt.Errorf("simulator %s: %w", s.Source, err)
		}
		files[s.Source] = src
	}

	dst := filepath.Join(dir, devprofile.ProjectDir)
	if _, err := os.Stat(filepath.Join(dst, id+".yaml")); err == nil {
		return fmt.Errorf("%s is already in %s/ (it is the project's pinned copy; naut devices update shows what changed)", id, devprofile.ProjectDir)
	}
	// A simulator two profiles share (abb-drives.st) may already be there;
	// it must be the same file, not a different one of the same name.
	for name, data := range files {
		if have, err := os.ReadFile(filepath.Join(dst, name)); err == nil && !bytes.Equal(have, data) {
			return fmt.Errorf("%s/%s is already there and differs from %s's", devprofile.ProjectDir, name, id)
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dst, name), files[name], 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "added %s\n", filepath.ToSlash(filepath.Join(devprofile.ProjectDir, name)))
	}
	ver := ""
	if p.Version != 0 {
		ver = fmt.Sprintf(" version %d", p.Version)
	}
	fmt.Fprintf(w, "%s%s: declare a device with it in %s:\n  devices:\n    - { name: <Name>, profile: %s, endpoint: <host> }\n", id, ver, project.ManifestName, id)
	return nil
}

// runDevicesRender prints what a project's devices: derive at load.
func runDevicesRender(args []string, w io.Writer) int {
	dir, ok := devicesDir("render", args)
	if !ok {
		return 2
	}
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut devices render:", err)
		return 1
	}
	set := m.DeviceSet()
	if set.Len() == 0 {
		fmt.Fprintf(w, "%s declares no devices:\n", project.ManifestName)
		return 0
	}
	src, err := set.TypesST()
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut devices render:", err)
		return 1
	}
	fmt.Fprintf(w, "# types\n\n%s\n", strings.TrimSpace(src))

	fmt.Fprint(w, "\n# tags\n\n")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TAG\tROLE\tTYPE\tDESCRIPTION")
	for _, t := range set.Tags() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, t.Role, t.Type, t.Desc)
	}
	tw.Flush()

	mm := set.ModbusManifest()
	fmt.Fprint(w, "\n# modbus\n\n")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tHOST\tPORT\tUNIT\tWORD ORDER")
	for _, s := range mm.Sources {
		order := s.WordOrder
		if order == "" {
			order = "big"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", s.ID, s.Host, s.Port, s.UnitID, order)
	}
	tw.Flush()
	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "BINDING\tACCESS\tTABLE\tADDRESS\tFORMAT")
	for _, b := range mm.Tags {
		access := "read"
		if b.Writable {
			access = "write"
		}
		format := b.Format
		if b.Scale != 0 && b.Scale != 1 {
			format += fmt.Sprintf(" ×%g", b.Scale)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", b.Name, access, b.Table, b.Address, format)
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%s.<point> bindings are the struct's members (read back); %s_<point> are the command tags.\n", "<device>", "<device>")
	return 0
}
