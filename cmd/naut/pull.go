package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/apiclient"
	"github.com/joyautomation/nautilus/internal/stproject"
)

const pullUsage = `naut pull — bring a controller's online edits back into the project

A controller running an online edit (see the VS Code "Download Program to
Controller" command) holds source that differs from the committed files.
Pull writes that source back into your program files so you can review it
with git and commit — the inverse of download, and the path from a field
edit to CI/CD.

Multi-task controllers pull every program: each is matched to the workspace
file declaring the same PROGRAM name (the POU name is a program's identity),
and a program with no local counterpart is written to <POU>.st / <POU>.fbd.

Only program files are rewritten; generated type files (eip_types.st) are
never touched — those mirror the device, so re-import them instead.

A live I/O configuration (an apply of drivers:, devices: and the files they
name, not a program edit) that the controller runs instead of its manifest's
is pulled with --io: the project files it was read from are written back so
you can review them with git and commit. A file you edited locally since the
build is refused (named) unless --force. A standby has nothing to pull: ask
the leader (or the service address).

Usage:
  naut pull --host <controller> [--dir <project>] [--check] [--io]
            [--dry-run] [--force] [--new]

Flags:
  --host    Controller hostname or IP serving the tag API (required).
            A controller with a token answers a name not in its
            NAUTILUS_ALLOWED_HOSTS only with the token: set NAUTILUS_TOKEN
            here and naut pull sends it.
  --port    Tag API port (default 8080)
  --dir     Project directory (default ".")
  --check   Report drift and exit non-zero if the controller differs;
            writes nothing. For CI — fail a build when a controller has
            un-pulled field edits (with --io, or I/O changes).
  --io      Also pull the live I/O configuration into the project files.
  --dry-run Print what would change as a unified diff and write nothing
            (exit 0). --check keeps its meaning and wins over it.
  --new     With --io, also write files that only the controller's manifest
            names (a driver an apply added). They are listed first; without
            --new such a pull writes nothing. Only .yaml/.yml files are ever
            written.
  --force   With --io, overwrite a project file you edited locally. It skips
            the one check that your copy is the file the controller booted
            from (it trusts the controller's word for that), so your edits to
            that file are replaced with no diff shown; each such path is
            printed. Only the files the manifest names as I/O files are ever
            written, with or without --force.
`

// programInfo mirrors the server's GET /api/program response — the selected
// program plus the resource directory.
type programInfo struct {
	Task     string `json:"task"`
	POU      string `json:"pou"`
	Source   string `json:"source"`
	Language string `json:"language"`
	Hash     string `json:"hash"`
	Dirty    bool   `json:"dirty"`
	Programs []struct {
		Task     string `json:"task"`
		POU      string `json:"pou"`
		Language string `json:"language"`
		Hash     string `json:"hash"`
		Dirty    bool   `json:"dirty"`
	} `json:"programs"`
}

func runPull(args []string) int {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	host := fs.String("host", "", "controller hostname or IP")
	port := fs.Int("port", 8080, "tag API port")
	dir := fs.String("dir", ".", "project directory")
	check := fs.Bool("check", false, "report drift only, write nothing")
	withIO := fs.Bool("io", false, "also pull the live I/O configuration")
	dry := fs.Bool("dry-run", false, "print the changes as a diff, write nothing")
	force := fs.Bool("force", false, "with --io, overwrite locally edited files")
	fresh := fs.Bool("new", false, "with --io, also write files only the controller's manifest names")
	fs.Usage = func() { fmt.Fprint(os.Stderr, pullUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "naut pull: --host is required")
		return 2
	}

	if (*force || *fresh) && !*withIO {
		fmt.Fprintln(os.Stderr, "naut pull: --force and --new apply to --io only")
		return 2
	}
	code := pullPrograms(*host, *port, *dir, *check, *dry)
	if *withIO {
		if c := pullIO(*host, *port, *dir, *check, *dry, *force, *fresh); c == 2 || (c == 1 && code == 0) {
			code = c
		}
	}
	return code
}

// pullPrograms pulls the controller's program edits (0 in sync or written,
// 1 drift under --check, 2 failure).
func pullPrograms(host string, port int, dir string, check, dry bool) int {
	root, err := fetchProgram(host, port, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut pull:", err)
		return 2
	}

	m, err := stproject.ComposeAll(dir, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut pull:", err)
		return 2
	}
	// Local programs by POU (the matching identity), case-insensitive.
	local := map[string]*stproject.ProgramFile{}
	for i := range m.Programs {
		if p := m.Programs[i].POU; p != "" {
			local[strings.ToLower(p)] = &m.Programs[i]
		}
	}

	// Older controllers (no directory) and single-program resources keep
	// the classic behavior: the one program pulls into the one program
	// file, whatever either is named — renames stay pullable.
	if len(root.Programs) <= 1 {
		if len(m.Programs) == 0 {
			fmt.Fprintf(os.Stderr, "naut pull: no .st or .fbd file with a PROGRAM in %s\n", dir)
			return 2
		}
		if len(m.Programs) > 1 {
			fmt.Fprintf(os.Stderr, "naut pull: multiple program files in %s but the controller runs one program — remove the extras or match their PROGRAM names to the controller's tasks\n", dir)
			return 2
		}
		return pullOne(root, &m.Programs[0], m.Prelude, dir, host, check, dry)
	}

	// Multi-task: pull every program, matched by POU name.
	drift, failures := 0, 0
	matched := map[string]bool{}
	for _, ps := range root.Programs {
		info := root
		if ps.Task != root.Task {
			full, err := fetchProgram(host, port, ps.Task)
			if err != nil {
				fmt.Fprintf(os.Stderr, "naut pull: task %s: %v\n", ps.Task, err)
				failures++
				continue
			}
			info = full
		}
		target := local[strings.ToLower(ps.POU)]
		if target != nil {
			matched[strings.ToLower(ps.POU)] = true
		} else {
			// A program with no local counterpart gets its own file.
			ext := ".st"
			if ps.Language == "fbd" {
				ext = ".fbd"
			}
			target = &stproject.ProgramFile{File: ps.POU + ext, POU: ps.POU}
			fmt.Printf("%s (task %s) has no local file — pulling into %s\n", ps.POU, ps.Task, target.File)
		}
		switch pullOne(info, target, m.Prelude, dir, host, check, dry) {
		case 1:
			drift++
		case 2:
			failures++
		}
	}
	for _, p := range m.Programs {
		if p.POU != "" && !matched[strings.ToLower(p.POU)] {
			fmt.Printf("note: %s (PROGRAM %s) has no counterpart on the controller\n", p.File, p.POU)
		}
	}
	if failures > 0 {
		return 2
	}
	if drift > 0 {
		if check {
			fmt.Fprintln(os.Stderr, "run `naut pull` (without --check) to update")
		}
		return 1
	}
	return 0
}

// pullOne reconciles one controller program with one workspace file.
// Returns 0 for in-sync/updated, 1 for drift under --check, 2 for failure.
func pullOne(info programInfo, target *stproject.ProgramFile, prelude, dir, host string, check, dry bool) int {
	program, ok := stproject.SplitProgram(info.Source, prelude)
	if !ok {
		// A program composed WITHOUT libraries (a task with none) serves
		// bare source — accept it whole when nothing precedes the PROGRAM.
		// Anything else ahead of it is a lib set this workspace doesn't
		// have, and that's a real mismatch to surface, not paper over.
		if idx := pouStart(info.Source, info.POU); idx >= 0 && strings.TrimSpace(info.Source[:idx]) == "" {
			program, ok = info.Source[idx:], true
		}
	}
	if !ok {
		fmt.Fprintf(os.Stderr,
			"naut pull: the controller's library/type sources for %s differ from %s — "+
				"reconcile the library files (or re-run `naut eip import`) before pulling.\n",
			progName(info, target), dir)
		return 2
	}
	if program == target.Body {
		fmt.Printf("%s is already in sync with %s (running %s)\n", target.File, host, info.Hash)
		return 0
	}
	if check {
		fmt.Printf("%s: %s differs from the controller (running %s)\n", host, target.File, info.Hash)
		return 1
	}
	if dry {
		was, _ := os.ReadFile(filepath.Join(dir, target.File))
		fmt.Print(unifiedDiff(target.File, target.File+" ("+host+")", string(was), program))
		return 0
	}
	if err := os.WriteFile(filepath.Join(dir, target.File), []byte(program), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut pull:", err)
		return 2
	}
	fmt.Printf("updated %s from %s (running %s)\n", target.File, host, info.Hash)
	fmt.Println("review with `git diff` and commit to make the edit permanent")
	return 0
}

func progName(info programInfo, target *stproject.ProgramFile) string {
	if info.POU != "" {
		return "PROGRAM " + info.POU
	}
	return target.File
}

// pouStart is the byte offset where `PROGRAM <pou>` begins in src, -1 if
// absent (or the POU is unknown).
func pouStart(src, pou string) int {
	if pou == "" {
		return -1
	}
	re := regexp.MustCompile(`(?im)^[ \t]*PROGRAM[ \t]+` + regexp.QuoteMeta(pou) + `\b`)
	if loc := re.FindStringIndex(src); loc != nil {
		return loc[0]
	}
	return -1
}

// fetchProgram GETs a program's info; task selects one on a multi-task
// controller ("" = the main program, which also carries the directory).
func fetchProgram(host string, port int, task string) (programInfo, error) {
	url := fmt.Sprintf("http://%s:%d/api/program", host, port)
	if task != "" {
		url += "?task=" + task
	}
	client := http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return programInfo{}, err
	}
	// --host by name reaches a token controller only with its token (#819).
	apiclient.Authorize(req)
	resp, err := client.Do(req)
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
