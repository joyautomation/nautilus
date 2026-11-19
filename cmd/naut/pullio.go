package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/apiclient"
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
	}
	var changes []change
	for _, f := range st.Files {
		// The controller chooses these paths: keep them inside dir.
		if !filepath.IsLocal(filepath.FromSlash(f.Path)) {
			return fail("%s names a file outside the project: %q", host, f.Path)
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		was, err := os.ReadFile(dst)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return fail("%v", err)
		}
		if exists && string(was) == f.Running {
			continue
		}
		c := change{file: f, dst: dst, local: string(was), exists: exists}
		if exists {
			sum := sha256.Sum256(was)
			c.refused = !force && hex.EncodeToString(sum[:]) != f.Booted
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
		if err := os.MkdirAll(filepath.Dir(c.dst), 0o755); err != nil {
			return fail("%v", err)
		}
		if err := os.WriteFile(c.dst, []byte(c.file.Running), 0o644); err != nil {
			return fail("%v", err)
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
