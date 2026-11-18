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
	from := fl.String("from", "", "copy from this folder of profiles instead of the built-in library (symlinks in it are followed, as cp does)")
	// Flags may come before or after the profile (add <profile> --from dir).
	var pos []string
	for rest := args; ; {
		if err := fl.Parse(rest); err != nil {
			return 2
		}
		if fl.NArg() == 0 {
			break
		}
		pos = append(pos, fl.Arg(0))
		rest = fl.Args()[1:]
	}
	if len(pos) < 1 || len(pos) > 2 {
		fmt.Fprint(os.Stderr, "usage: naut devices add <profile> [project-dir] [--from dir]\n")
		return 2
	}
	id, dir := pos[0], "."
	if len(pos) == 2 {
		dir = pos[1]
	}
	if err := addProfile(dir, id, *from, w); err != nil {
		fmt.Fprintln(os.Stderr, "naut devices add:", err)
		return 1
	}
	return 0
}

func addProfile(dir, id, from string, w io.Writer) error {
	if !devprofile.IsID(id) {
		return fmt.Errorf("%q is not a profile id (letters, digits, '-', '_', '.'; no directories)", id)
	}
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
	// profiles/ must be the project's own folder: a symlink there would
	// send the copy wherever it points.
	if fi, err := os.Lstat(dst); err == nil && !fi.IsDir() {
		return fmt.Errorf("%s/ is not a folder (a symlink?): add writes only into the project's own %s/", devprofile.ProjectDir, devprofile.ProjectDir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(dst, id+".yaml")); err == nil {
		return fmt.Errorf("%s is already in %s/: that copy is the project's pin, and add never replaces it (to take a newer one, compare the two and replace the file yourself; naut devices update will do that)", id, devprofile.ProjectDir)
	}
	// Check every destination before writing any. A simulator two profiles
	// share (abb-drives.st) may already be there; it must be the same
	// regular file, not a different one or a symlink.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var write []string
	for _, name := range names {
		switch same, err := existingCopy(dst, name, files[name]); {
		case err != nil:
			return err
		case !same:
			write = append(write, name)
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, name := range write {
		if err := createInDir(dst, name, files[name]); err != nil {
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

// existingCopy reports whether dir/name is already a regular file holding
// data. It refuses a name that is not a plain file name (whatever a source
// file says, the copy lands in dir itself), a symlink (dangling or not: a
// write through it would land outside dir), and a different file.
func existingCopy(dir, name string, data []byte) (bool, error) {
	if !devprofile.IsFileName(name) {
		return false, fmt.Errorf("%q would be written outside %s/", name, devprofile.ProjectDir)
	}
	fi, err := os.Lstat(filepath.Join(dir, name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case !fi.Mode().IsRegular():
		return false, fmt.Errorf("%s/%s is a symlink or not a regular file: add writes only regular files in %s/", devprofile.ProjectDir, name, devprofile.ProjectDir)
	}
	have, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return false, err
	}
	if !bytes.Equal(have, data) {
		return false, fmt.Errorf("%s/%s is already there and differs", devprofile.ProjectDir, name)
	}
	return true, nil
}

// createInDir creates dir/name, which must not exist yet. O_EXCL fails on
// any existing path, a symlink included (dangling or not), so a symlink
// planted after existingCopy looked is not followed either.
func createInDir(dir, name string, data []byte) error {
	if !devprofile.IsFileName(name) {
		return fmt.Errorf("%q would be written outside %s/", name, devprofile.ProjectDir)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
