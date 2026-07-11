package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/joyautomation/nautilus/internal/stproject"
)

const pullUsage = `nautilus pull — bring a controller's online edits back into the project

A controller running an online edit (see the VS Code "Download Program to
Controller" command) holds source that differs from the committed files.
Pull writes that source back into your program file so you can review it with
git and commit — the inverse of download, and the path from a field edit to
CI/CD.

Only the program file is rewritten; generated type files (eip_types.st) are
never touched — those mirror the device, so re-import them instead.

Usage:
  nautilus pull --host <controller> [--dir <project>] [--check]

Flags:
  --host    Controller hostname or IP serving the tag API (required)
  --port    Tag API port (default 8080)
  --dir     Project directory (default ".")
  --check   Report drift and exit non-zero if the controller differs;
            writes nothing. For CI — fail a build when a controller has
            un-pulled field edits.
`

type programInfo struct {
	Source string `json:"source"`
	Hash   string `json:"hash"`
	Dirty  bool   `json:"dirty"`
}

func runPull(args []string) int {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	host := fs.String("host", "", "controller hostname or IP")
	port := fs.Int("port", 8080, "tag API port")
	dir := fs.String("dir", ".", "project directory")
	check := fs.Bool("check", false, "report drift only, write nothing")
	fs.Usage = func() { fmt.Fprint(os.Stderr, pullUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "nautilus pull: --host is required")
		return 2
	}

	info, err := fetchProgram(*host, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus pull:", err)
		return 2
	}

	comp, err := stproject.Compose(*dir, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus pull:", err)
		return 2
	}

	program, ok := stproject.SplitProgram(info.Source, comp.Prelude)
	if !ok {
		fmt.Fprintf(os.Stderr,
			"nautilus pull: the controller's library/type sources differ from %s — "+
				"reconcile the generated type files (re-run `nautilus eip import`) before pulling the program.\n", *dir)
		return 1
	}

	target := filepath.Join(*dir, comp.ProgramFile)
	if program == comp.ProgramBody {
		fmt.Printf("%s is already in sync with %s (running %s)\n", comp.ProgramFile, *host, info.Hash)
		return 0
	}

	if *check {
		fmt.Printf("%s: %s differs from the controller (running %s)\n", *host, comp.ProgramFile, info.Hash)
		fmt.Fprintln(os.Stderr, "run `nautilus pull` (without --check) to update it")
		return 1
	}

	if err := os.WriteFile(target, []byte(program), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus pull:", err)
		return 2
	}
	fmt.Printf("updated %s from %s (running %s)\n", comp.ProgramFile, *host, info.Hash)
	fmt.Println("review with `git diff` and commit to make the edit permanent")
	return 0
}

func fetchProgram(host string, port int) (programInfo, error) {
	url := fmt.Sprintf("http://%s:%d/api/program", host, port)
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return programInfo{}, fmt.Errorf("no controller at %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return programInfo{}, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return programInfo{}, err
	}
	var info programInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return programInfo{}, fmt.Errorf("bad response from %s: %w", url, err)
	}
	return info, nil
}
