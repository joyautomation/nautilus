package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/apiclient"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/server"
)

// pullIO writes the project files a controller's live I/O configuration
// differs in back into dir (#706). Returns 0 in sync or written, 1 drift
// under --check, 2 failure or a refusal. Nothing is written unless every
// file can be.
func pullIO(host string, port int, dir string, check, dry, force bool) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "naut pull --io: "+format+"\n", a...)
		return 2
	}
	st, err := fetchIOFiles(host, port)
	if err != nil {
		return fail("%v", err)
	}
	if !st.Leading {
		return fail("%s is a standby: its running I/O configuration is not the one in force. Ask the leader, or the service address", host)
	}
	if len(st.Units) == 0 {
		fmt.Printf("%s runs the manifest's I/O configuration: nothing to pull\n", host)
		return 0
	}
	if len(st.Conflicts) > 0 {
		return fail("%s holds two different contents for %s (from different applies): re-apply one configuration, then pull", host, strings.Join(st.Conflicts, ", "))
	}
	var names []string
	for _, u := range st.Units {
		n := u.Driver
		if u.Device != "" {
			n += "/" + u.Device
		}
		names = append(names, n)
	}
	if st.NonIO {
		fmt.Fprintln(os.Stderr, "naut pull --io: warning: the applied manifest also changes more than I/O (the program, retain, ...). Only drivers:, devices: and the files they name ran; the rest of nautilus.yaml is written back too and takes effect only when you deploy it.")
	}

	type change struct {
		file    server.IOFile
		dst     string
		local   string
		exists  bool
		refused bool
		edited  bool // differs from the booted file (written only with --force)
	}
	// The controller chooses these paths and bytes, so none of them is
	// trusted: only files this project's own manifest names are written
	// (never program sources, hidden paths or .git), and every read and
	// write goes through os.Root, which refuses a symlink out of the project.
	allowed := ioPullAllowed(dir, st.Files)
	var disallowed []string
	for _, f := range st.Files {
		if !allowed[f.Path] {
			disallowed = append(disallowed, f.Path)
		}
	}
	if len(disallowed) > 0 {
		return fail("%s offers files this project's manifest does not name as I/O files (only %s, the manifests drivers: name and profiles/<id>.yaml of devices: are pulled; never hidden paths, .git or program sources), so nothing was written: %s", host, project.ManifestName, strings.Join(disallowed, ", "))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fail("%v", err)
	}
	defer root.Close()
	var changes []change
	for _, f := range st.Files {
		dst := filepath.FromSlash(f.Path)
		was, err := root.ReadFile(dst)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return fail("%s: %v", f.Path, err)
		}
		if exists && string(was) == f.Running {
			continue
		}
		c := change{file: f, dst: dst, local: string(was), exists: exists}
		if exists {
			sum := sha256.Sum256(was)
			c.edited = hex.EncodeToString(sum[:]) != f.Booted
			c.refused = !force && c.edited
		}
		changes = append(changes, c)
	}
	if len(changes) == 0 {
		fmt.Printf("the project files already hold %s's I/O configuration (%s)\n", host, strings.Join(names, ", "))
		return 0
	}
	if check {
		for _, c := range changes {
			fmt.Printf("%s: %s differs from the controller's running I/O configuration (%s)\n", host, c.file.Path, strings.Join(names, ", "))
		}
		fmt.Fprintln(os.Stderr, "run `naut pull --io` (without --check) to update")
		return 1
	}
	if dry {
		for _, c := range changes {
			note := ""
			if c.refused {
				note = " (edited locally since the build: a pull needs --force)"
			}
			fmt.Printf("# %s%s\n", c.file.Path, note)
			fmt.Print(unifiedDiff(c.file.Path, c.file.Path+" ("+host+")", c.local, c.file.Running))
		}
		return 0
	}
	var refused []string
	for _, c := range changes {
		if c.refused {
			refused = append(refused, c.file.Path)
		}
	}
	if len(refused) > 0 {
		return fail("not the files the controller booted from, so pulling would overwrite your edits: %s. Review with --dry-run, or pass --force", strings.Join(refused, ", "))
	}
	for _, c := range changes {
		if c.edited {
			fmt.Fprintf(os.Stderr, "naut pull --io: --force: overwriting %s, which differs from the file the controller booted from\n", c.file.Path)
		}
	}
	for _, c := range changes {
		if err := root.MkdirAll(filepath.Dir(c.dst), 0o755); err != nil {
			return fail("%s: %v", c.file.Path, err)
		}
		if err := root.WriteFile(c.dst, []byte(c.file.Running), 0o644); err != nil {
			return fail("%s: %v", c.file.Path, err)
		}
		fmt.Printf("updated %s from %s (I/O: %s)\n", c.file.Path, host, strings.Join(names, ", "))
	}
	fmt.Println("review with `git diff` and commit to make the change permanent")
	return 0
}

// fetchIOFiles GETs a controller's /api/io/files.
func fetchIOFiles(host string, port int) (server.IOFiles, error) {
	var out server.IOFiles
	url := fmt.Sprintf("http://%s:%d/api/io/files", host, port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return out, err
	}
	apiclient.Authorize(req)
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("no controller at %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("%s returned %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("bad response from %s: %w", url, err)
	}
	return out, nil
}

// ioPullAllowed is the project paths a pull may write: the manifest, the
// manifest files its drivers: name and the profiles its devices: use, as
// the local manifest and the one being pulled name them, and only plain
// data files (.yaml, .yml, .json) with no hidden path component. Computed
// here: the controller's list of paths is never what grants a write.
func ioPullAllowed(dir string, files []server.IOFile) map[string]bool {
	names := map[string]bool{project.ManifestName: true}
	add := func(m *project.Manifest) {
		if m == nil {
			return
		}
		if m.Driver.Manifest != "" {
			names[path.Clean(m.Driver.Manifest)] = true
		}
		for _, d := range m.Drivers {
			if d.Manifest != "" {
				names[path.Clean(d.Manifest)] = true
			}
		}
		for _, d := range m.Devices {
			if d.Profile != "" {
				names["profiles/"+d.Profile+".yaml"] = true
			}
		}
	}
	base := os.DirFS(dir)
	if m, err := project.ReadManifest(base, ""); err == nil {
		add(m)
	}
	for _, f := range files {
		if f.Path == project.ManifestName {
			overlay := project.OverlayFS(base, map[string][]byte{project.ManifestName: []byte(f.Running)})
			if m, err := project.ReadManifest(overlay, ""); err == nil {
				add(m)
			}
		}
	}
	out := map[string]bool{}
	for _, f := range files {
		if names[f.Path] && ioPullSafePath(f.Path) {
			out[f.Path] = true
		}
	}
	return out
}

// ioPullSafePath: a clean relative slash path inside the project, of a
// data file, with no hidden component.
func ioPullSafePath(p string) bool {
	if p == "" || path.Clean(p) != p || !filepath.IsLocal(filepath.FromSlash(p)) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".yaml", ".yml", ".json":
		return true
	}
	return false
}
