package main

// naut devices update: compare a project's vendored profiles with their
// source, and replace them on request (docs/design/device-library.md §1).

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"

	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/project"
)

// releasedProfiles lists, for the built-in library, the hash of every
// (profile file + simulator) pair a naut released: `id version hash`, a
// trailing `history` marking a pair from before this list was kept. A
// vendored copy whose hash is listed is an unedited copy of a release; one
// whose hash is not has been edited by hand.
//
//go:embed devices_released.txt
var releasedProfiles string

// pairHash identifies a profile file and the simulator beside it by their
// bytes. sim is nil for a profile with none.
func pairHash(yaml, sim []byte) string {
	h := sha256.New()
	h.Write(yaml)
	h.Write([]byte{0})
	h.Write(sim)
	return hex.EncodeToString(h.Sum(nil))
}

// releasedHashes returns the hashes listed for one built-in profile id.
func releasedHashes(id string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(releasedProfiles, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == id && !strings.HasPrefix(f[0], "#") {
			out[f[2]] = true
		}
	}
	return out
}

// profileFiles is a profile's files by name: <id>.yaml and its simulator.
type profileFiles map[string][]byte

func (f profileFiles) hash(id string) string {
	p, err := devprofile.ParseProfile(f[id+".yaml"], id+".yaml")
	var sim []byte
	if err == nil && p.Simulator != nil {
		sim = f[p.Simulator.Source]
	}
	return pairHash(f[id+".yaml"], sim)
}

// produced renders what a profile derives for one device of it, through
// the same code `naut devices render` uses, so "the same" here means the
// same tags, bindings, derived points and alarms, not the same YAML.
func (f profileFiles) produced(id string) (string, *devprofile.Profile, error) {
	fsys := fstest.MapFS{}
	for name, data := range f {
		fsys[devprofile.ProjectDir+"/"+name] = &fstest.MapFile{Data: data}
	}
	set, err := devbind.Resolve(fsys, []devbind.Device{{Name: "DEV", Profile: id, Endpoint: "192.0.2.1"}})
	if err != nil {
		return "", nil, err
	}
	var b bytes.Buffer
	if err := renderSet(&b, set, nil); err != nil {
		return "", nil, err
	}
	ps := set.Profiles()
	if len(ps) != 1 {
		return "", nil, fmt.Errorf("profile %s did not resolve", id)
	}
	return b.String(), ps[0], nil
}

// readProfileFiles reads <id>.yaml and its simulator with read.
func readProfileFiles(id string, read func(string) ([]byte, error)) (profileFiles, error) {
	raw, err := read(id + ".yaml")
	if err != nil {
		return nil, err
	}
	p, err := devprofile.ParseProfile(raw, id+".yaml")
	if err != nil {
		return nil, err
	}
	out := profileFiles{id + ".yaml": raw}
	if s := p.Simulator; s != nil {
		src, err := read(s.Source)
		if err != nil {
			return nil, fmt.Errorf("simulator %s: %w", s.Source, err)
		}
		out[s.Source] = src
	}
	return out, nil
}

// lineDiff is a unified-style list of the lines two texts differ by
// (longest common subsequence), "- old" and "+ new", with no context.
func lineDiff(a, b string) []string {
	x, y := strings.Split(strings.TrimRight(a, "\n"), "\n"), strings.Split(strings.TrimRight(b, "\n"), "\n")
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			i++
			j++
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, "+ "+y[j])
			j++
		default:
			out = append(out, "- "+x[i])
			i++
		}
	}
	return out
}

// updateItem is one vendored profile's comparison with its source.
type updateItem struct {
	id              string
	copyF, srcF     profileFiles
	copyVer, srcVer int
	diff            []string // what the project would derive differently
	filesDiffer     bool     // some file's bytes differ (docs, notes, simulator)
	edited          bool     // the copy matches no release of the source
	fromFolder      bool     // the source is a --from folder, which keeps no releases
	err             error
}

func (u *updateItem) upToDate() bool { return !u.filesDiffer }

// runDevicesUpdate compares each vendored profile (or the named one) with
// its source and reports what would change; --yes replaces the copies.
func runDevicesUpdate(args []string, w io.Writer) int {
	fl := flag.NewFlagSet("devices update", flag.ContinueOnError)
	from := fl.String("from", "", "compare with this folder of profiles instead of the built-in library")
	yes := fl.Bool("yes", false, "replace the copies that differ (never a hand-edited copy without --discard-edits)")
	discard := fl.Bool("discard-edits", false, "with --yes, replace a hand-edited copy too: its edits are lost")
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
	if len(pos) > 2 {
		fmt.Fprint(os.Stderr, "usage: naut devices update [<profile>] [project-dir] [--from dir] [--yes] [--discard-edits]\n")
		return 2
	}
	if *discard && !*yes {
		fmt.Fprintln(os.Stderr, "naut devices update: --discard-edits goes with --yes")
		return 2
	}
	// A lone argument is a profile id when profiles/ has it, else a project
	// directory.
	id, dir := "", "."
	switch len(pos) {
	case 1:
		if fi, err := os.Stat(pos[0]); err == nil && fi.IsDir() {
			dir = pos[0]
		} else {
			id = pos[0]
		}
	case 2:
		id, dir = pos[0], pos[1]
	}
	if err := updateProfiles(dir, id, *from, *yes, *discard, w); err != nil {
		fmt.Fprintln(os.Stderr, "naut devices update:", err)
		return 1
	}
	return 0
}

// errUpdateRefused marks a refusal that has already been explained on w.
var errUpdateRefused = errors.New("nothing was replaced")

func updateProfiles(dir, only, from string, yes, discard bool, w io.Writer) error {
	if _, err := os.Stat(filepath.Join(dir, project.ManifestName)); err != nil {
		return fmt.Errorf("%s is not a project (no %s)", dir, project.ManifestName)
	}
	pdir := filepath.Join(dir, devprofile.ProjectDir)
	if fi, err := os.Lstat(pdir); err == nil && !fi.IsDir() {
		return fmt.Errorf("%s/ is not a folder (a symlink?)", devprofile.ProjectDir)
	}
	ids, err := vendoredIDs(pdir, only)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Fprintf(w, "no vendored profiles in %s/ (naut devices add copies one)\n", devprofile.ProjectDir)
		return nil
	}
	readSrc := devprofile.BuiltinFile
	if from != "" {
		readSrc = func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(from, name)) }
	}
	readCopy := func(name string) ([]byte, error) {
		fi, err := os.Lstat(filepath.Join(pdir, name))
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s/%s is a symlink or not a regular file", devprofile.ProjectDir, name)
		}
		return os.ReadFile(filepath.Join(pdir, name))
	}

	var items []*updateItem
	for _, id := range ids {
		u := &updateItem{id: id}
		items = append(items, u)
		if u.copyF, u.err = readProfileFiles(id, readCopy); u.err != nil {
			u.err = fmt.Errorf("the project's copy: %w", u.err)
			continue
		}
		if u.srcF, u.err = readProfileFiles(id, readSrc); u.err != nil {
			if errors.Is(u.err, fs.ErrNotExist) {
				u.err = fmt.Errorf("the source has no profile %s (a profile of your own is not updated)", id)
			}
			continue
		}
		var cp, sp *devprofile.Profile
		var ct, st string
		if ct, cp, u.err = u.copyF.produced(id); u.err != nil {
			u.err = fmt.Errorf("the project's copy: %w", u.err)
			continue
		}
		if st, sp, u.err = u.srcF.produced(id); u.err != nil {
			u.err = fmt.Errorf("the source: %w", u.err)
			continue
		}
		u.copyVer, u.srcVer = cp.Version, sp.Version
		u.diff = lineDiff(ct, st)
		u.filesDiffer = !sameFiles(u.copyF, u.srcF)
		// Source file names are the source's to choose: only a simulator
		// (.st) beside the profile, and never a file already in profiles/
		// that is not this profile's own.
		for n, data := range u.srcF {
			if n == id+".yaml" {
				continue
			}
			if !strings.HasSuffix(n, ".st") {
				u.err = fmt.Errorf("the source's simulator %s is not a .st file", n)
				break
			}
			if _, mine := u.copyF[n]; mine {
				continue
			}
			if have, err := os.Lstat(filepath.Join(pdir, n)); err == nil {
				if have.Mode().IsRegular() {
					if cur, err := os.ReadFile(filepath.Join(pdir, n)); err == nil && bytes.Equal(cur, data) {
						continue
					}
				}
				u.err = fmt.Errorf("the source's simulator %s would replace a file in %s/ that is not this profile's", n, devprofile.ProjectDir)
				break
			}
		}
		if u.err != nil {
			continue
		}
		if u.filesDiffer {
			// A copy is unedited only when it is byte-for-byte a release of
			// the built-in library. A --from folder keeps no releases of its
			// own, so a copy of one of its profiles that is not a built-in
			// release is treated as edited too: it is never replaced without
			// --discard-edits.
			u.edited = !releasedHashes(id)[u.copyF.hash(id)]
			u.fromFolder = from != ""
		}
	}

	report(w, items)
	var replace []*updateItem
	blocked := false
	for _, u := range items {
		switch {
		case u.err != nil:
			blocked = true
		case u.upToDate():
		case u.edited && !discard:
			blocked = true
		default:
			replace = append(replace, u)
		}
	}
	if !yes {
		if len(replace) > 0 || blocked {
			fmt.Fprintln(w, "\nnothing was replaced: run again with --yes to replace the profiles above that differ")
		}
		return nil
	}
	if len(replace) == 0 {
		if blocked {
			return errUpdateRefused
		}
		return nil
	}
	if msg := sharedSimulatorProblem(pdir, replace); msg != "" {
		fmt.Fprintln(w, "\nrefused:", msg)
		return errUpdateRefused
	}
	if out, ok := checkWithReplaced(dir, replace); !ok {
		fmt.Fprintf(w, "\nrefused: the project's check fails with the new copies (it passed before):\n%s", out)
		return errUpdateRefused
	}
	for _, u := range replace {
		if err := replaceCopy(pdir, u); err != nil {
			return err
		}
		fmt.Fprintf(w, "replaced %s/%s.yaml\n", devprofile.ProjectDir, u.id)
	}
	if blocked {
		return errUpdateRefused
	}
	return nil
}

// vendoredIDs lists the ids of profiles/*.yaml, or just only when named.
func vendoredIDs(pdir, only string) ([]string, error) {
	if only != "" {
		if !devprofile.IsID(only) {
			return nil, fmt.Errorf("%q is not a profile id", only)
		}
		if _, err := os.Lstat(filepath.Join(pdir, only+".yaml")); err != nil {
			return nil, fmt.Errorf("%s/%s.yaml is not in the project (naut devices add copies a profile in)", devprofile.ProjectDir, only)
		}
		return []string{only}, nil
	}
	ents, err := os.ReadDir(pdir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".yaml") && devprofile.IsID(strings.TrimSuffix(n, ".yaml")) {
			ids = append(ids, strings.TrimSuffix(n, ".yaml"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func sameFiles(a, b profileFiles) bool {
	if len(a) != len(b) {
		return false
	}
	for n, d := range a {
		if !bytes.Equal(d, b[n]) {
			return false
		}
	}
	return true
}

func report(w io.Writer, items []*updateItem) {
	for _, u := range items {
		switch {
		case u.err != nil:
			fmt.Fprintf(w, "%s: cannot compare: %v\n", u.id, u.err)
		case u.upToDate():
			fmt.Fprintf(w, "%s: up to date (version %d)\n", u.id, u.copyVer)
		default:
			fmt.Fprintf(w, "%s: version %d -> %d\n", u.id, u.copyVer, u.srcVer)
			if len(u.diff) == 0 {
				fmt.Fprintln(w, "  the project would derive the same tags, bindings and alarms; only the files differ (docs, notes or the simulator)")
			}
			for _, l := range u.diff {
				fmt.Fprintln(w, "  "+l)
			}
			if !sameSimulator(u) {
				fmt.Fprintln(w, "  the simulator file differs")
			}
			switch {
			case u.edited && u.fromFolder:
				fmt.Fprintf(w, "  HAND-EDITED, as far as naut can tell: the copy is not a release of a built-in %s, and a --from folder keeps no past releases to compare with. The lines above include any edits: they would be lost. Replace with --yes --discard-edits once you have checked them\n", u.id)
			case u.edited:
				fmt.Fprintf(w, "  HAND-EDITED: the copy matches no release of %s, so it has been changed in this project. The lines above include its edits: they would be lost. Keep them by moving them to a profile of your own, or replace anyway with --yes --discard-edits\n", u.id)
			case u.copyVer == u.srcVer:
				fmt.Fprintf(w, "  note: the source changed at the same version %d (a release of the source should bump version:)\n", u.copyVer)
			}
		}
	}
}

func sameSimulator(u *updateItem) bool {
	for n, d := range u.copyF {
		if strings.HasSuffix(n, ".yaml") {
			continue
		}
		if !bytes.Equal(d, u.srcF[n]) {
			return false
		}
	}
	return true
}

// sharedSimulatorProblem refuses to replace a simulator file another
// vendored profile still uses and is not being moved to the same source.
func sharedSimulatorProblem(pdir string, replace []*updateItem) string {
	moving := map[string]*updateItem{}
	for _, u := range replace {
		moving[u.id] = u
	}
	// Every vendored profile in the folder, not only those named.
	all, _ := vendoredIDs(pdir, "")
	for _, u := range replace {
		for name := range u.srcF {
			if strings.HasSuffix(name, ".yaml") || bytes.Equal(u.copyF[name], u.srcF[name]) {
				continue
			}
			for _, other := range all {
				if other == u.id || moving[other] != nil {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(pdir, other+".yaml"))
				if err != nil {
					return fmt.Sprintf("cannot read %s/%s.yaml to see whether it shares the simulator %s: %v", devprofile.ProjectDir, other, name, err)
				}
				p, err := devprofile.ParseProfile(raw, other+".yaml")
				if err != nil {
					return fmt.Sprintf("%s/%s.yaml does not parse, so naut cannot see whether it shares the simulator %s: %v", devprofile.ProjectDir, other, name, err)
				}
				if p.Simulator != nil && p.Simulator.Source == name {
					return fmt.Sprintf("%s is the simulator of %s and of %s; replacing it for one would change the other's. Update them together: naut devices update (no profile named)", name, u.id, other)
				}
			}
		}
	}
	return ""
}

// replaceCopy writes the source's files over the copy's: each to a new
// file beside it, then renamed over (a symlink is replaced, not followed).
func replaceCopy(pdir string, u *updateItem) error {
	// What was compared is what is replaced: a copy changed since is not.
	for n, was := range u.copyF {
		now, err := os.ReadFile(filepath.Join(pdir, n))
		if err != nil || (!bytes.Equal(now, was) && !bytes.Equal(now, u.srcF[n])) { // (a shared simulator may have been replaced a moment ago)
			return fmt.Errorf("%s/%s changed while update was running; nothing more was replaced", devprofile.ProjectDir, n)
		}
	}
	names := make([]string, 0, len(u.srcF))
	for n := range u.srcF {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if bytes.Equal(u.copyF[n], u.srcF[n]) {
			continue
		}
		tmp := "." + n + ".new"
		_ = os.Remove(filepath.Join(pdir, tmp))
		if err := createInDir(pdir, tmp, u.srcF[n]); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(pdir, tmp), filepath.Join(pdir, n)); err != nil {
			os.Remove(filepath.Join(pdir, tmp))
			return err
		}
	}
	return nil
}

// checkWithReplaced runs the project's check on a copy of the project with
// the new profiles in place. ok is false when it fails and the project's
// check as it stands passes; out is the check's output.
func checkWithReplaced(dir string, replace []*updateItem) (out string, ok bool) {
	before, beforeCode, err := runCheckCaptured(dir)
	if err != nil {
		return err.Error(), false
	}
	tmp, err := os.MkdirTemp("", "naut-update-check-")
	if err != nil {
		return err.Error(), false
	}
	defer os.RemoveAll(tmp)
	if err := copyProject(dir, tmp); err != nil {
		return err.Error(), false
	}
	for _, u := range replace {
		for n, d := range u.srcF {
			if err := os.WriteFile(filepath.Join(tmp, devprofile.ProjectDir, n), d, 0o644); err != nil {
				return err.Error(), false
			}
		}
	}
	after, code, err := runCheckCaptured(tmp)
	if err != nil {
		return err.Error(), false
	}
	after = strings.ReplaceAll(after, tmp, dir)
	if code == 0 {
		return "", true
	}
	if beforeCode == 0 {
		return after, false
	}
	// The project already fails its check: the update may not add to it.
	had := map[string]bool{}
	for _, l := range strings.Split(before, "\n") {
		had[l] = true
	}
	var added []string
	for _, l := range strings.Split(after, "\n") {
		if !had[l] && strings.TrimSpace(l) != "" {
			added = append(added, l)
		}
	}
	if len(added) == 0 {
		return "", true
	}
	return strings.Join(added, "\n") + "\n", false
}

// runCheckCaptured runs `naut check` on dir and returns what it printed.
// Standard output and error are put back however it ends.
func runCheckCaptured(dir string) (out string, code int, err error) {
	prevRender, prevVerbose := renderScreens, checkVerbose
	oo, oe := os.Stdout, os.Stderr
	r, wr, err := os.Pipe()
	if err != nil {
		return "", 0, fmt.Errorf("cannot run the project's check: %w", err)
	}
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		io.Copy(&b, r)
		done <- b.String()
	}()
	func() {
		os.Stdout, os.Stderr = wr, wr
		defer func() {
			os.Stdout, os.Stderr = oo, oe
			renderScreens, checkVerbose = prevRender, prevVerbose
			wr.Close()
		}()
		code = runCheck([]string{"-render=false", dir})
	}()
	return <-done, code, nil
}

// copyProject copies regular files of src into dst, leaving out .git and
// node_modules and anything that is not a plain file or folder.
func copyProject(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
}
