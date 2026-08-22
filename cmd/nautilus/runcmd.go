// `nautilus run` + `nautilus build`: the no-Go product surface. A manifest
// project (nautilus.yaml + IEC files) runs directly under the CLI binary,
// and `build` emits a deployable single binary by appending the project to
// a copy of this executable — no Go toolchain anywhere. Devs who outgrow
// the manifest drop to the Go library API (`nautilus new` without --no-go);
// that tier is unchanged.

package main

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/vcs"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// runProject hosts a loaded project: scan loop + tag API, Ctrl+C to stop.
// dir is the project's on-disk path when running from a checkout ("" for a
// built binary's embedded project) — it's where live git history capture
// looks; a built binary carries its history in the archive instead.
func runProject(fsys fs.FS, manifest, label, dir string) int {
	proj, err := project.Load(fsys, manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus run:", err)
		return 1
	}

	addr := proj.Addr
	if env := os.Getenv("NAUTILUS_ADDR"); env != "" {
		addr = env
	}

	// Redundancy and retained state wire in before the runtime compiles,
	// so the first scan already answers to the elector and sees what the
	// last leader saved. Outside a cluster the elector is standalone
	// (always leader) and the retain store is the JSON file — the same
	// manifest runs on a bench and as a replica set.
	var el *leader.Elector
	if proj.Redundancy != nil {
		el, err = leader.New(proj.LeaseName(), os.Getenv("POD_NAME"), selfAddr(addr))
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus run: redundancy:", err)
			return 1
		}
		proj.Runtime.Coordinator = el
	}
	if proj.Retain != nil {
		cm, file := proj.RetainNames()
		proj.Runtime.Retain = retain.New(cm, file)
	}

	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus run: compile:", err)
		return 1
	}

	// Alarms before the scan loop starts: the engine registers itself on
	// the runtime's post-scan hook, and SetAlarms hands it to the retain
	// saver, so the first takeover restores the acknowledgements the last
	// leader saved rather than re-annunciating them.
	alarms, err := proj.NewAlarms(rt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus run: alarms:", err)
		return 1
	}
	if alarms != nil {
		defer alarms.Close()
		rt.SetAlarms(alarms.Engine)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if el != nil {
		go el.Run(ctx)
		// On shutdown, hand the lease over instead of letting it expire:
		// a standby takes over on its next tick rather than 4s later.
		defer el.Release()
	}
	// Drivers with their own polling loop (eip) start here; the loopback
	// memory driver has nothing to start.
	if s, ok := proj.Runtime.Driver.(interface{ Start(context.Context) }); ok {
		s.Start(ctx)
	}
	go rt.Run(ctx)

	// Sparkplug B retransmission, when the manifest asks for it.
	spNode, err := proj.Sparkplug(rt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus run: sparkplug:", err)
		return 1
	}
	sparkplugUp := false
	if spNode != nil {
		if err := spNode.Start(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "sparkplug:", err, "(continuing without it)")
		} else {
			defer spNode.Stop()
			sparkplugUp = true
		}
	}

	sopts := proj.Server
	if tok := os.Getenv("NAUTILUS_TOKEN"); tok != "" {
		sopts.AuthToken = tok
	}
	if h := os.Getenv("NAUTILUS_HISTORIAN_URL"); h != "" {
		sopts.HistorianURL = h
	}
	// Field-driver + Sparkplug status feed GET /api/drivers and the stream.
	sopts.Drivers = proj.DriverStatus(spNode)
	if alarms != nil {
		sopts.Alarms = alarms.Engine
		sopts.AlarmShelveTimes = alarms.ShelveTimes
	}
	if el != nil {
		sopts.Cluster = el
	}
	// Program provenance: a built binary decodes the history its archive
	// carries; a checkout captures live from git. Lazy (first request pays,
	// not startup) and nil-safe — no repo just means an empty history.
	hist := loadHistory(fsys, dir)
	sopts.History = hist
	sopts.SourcesAt = func(sha string) (map[string]string, error) {
		snap, ok := vcs.SnapshotFS(hist(), sha)
		if !ok {
			return nil, fmt.Errorf("no file snapshot captured for %s", sha)
		}
		return project.Sources(snap, manifest)
	}
	srv := server.New(rt, sopts)
	go srv.Run(ctx)

	apiUp := false
	if ln, err := net.Listen("tcp", addr); err != nil {
		fmt.Fprintf(os.Stderr, "tag api: %v (continuing without it)\n", err)
	} else {
		apiUp = true
		go func() {
			if err := http.Serve(ln, srv.Handler()); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "tag api:", err)
			}
		}()
	}

	banner := "nautilus · " + proj.Name
	if label != "" {
		banner += " (" + label + ")"
	}
	if apiUp {
		if proj.HMIDir != "" {
			banner += " — hmi (" + proj.HMIDir + ") + tag API on http://" + addr
		} else {
			banner += " — dashboard + tag API on http://" + addr
		}
	}
	if sparkplugUp {
		banner += " — sparkplug up"
	}
	if alarms != nil {
		banner += fmt.Sprintf(" — %d alarms", len(alarms.Defs))
	}
	if proj.Runtime.Retain != nil {
		banner += " — retain: " + proj.Runtime.Retain.Kind()
	}
	if el != nil {
		banner += " — redundancy: " + el.Status().Mode
	}
	fmt.Println(banner + " — Ctrl+C to stop")
	<-ctx.Done()
	return 0
}

// selfAddr is what a standby needs to reach this replica: the pod IP from
// the downward API joined with the API's listen port. Empty outside a
// cluster — standalone mode has nobody to proxy to.
func selfAddr(addr string) string {
	ip := os.Getenv("POD_IP")
	if ip == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	return net.JoinHostPort(ip, port)
}

func runRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	manifest := fs.String("m", "", manifestFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	return runProject(os.DirFS(dir), *manifest, "", dir)
}

// historyMarker is the archive entry holding a built binary's captured git
// history (a server.ProgramHistory as JSON). Dot-named like .manifest: the
// archive walk skips dotfiles, so nothing in a project can collide with it,
// and `unzip -p <binary> .history` answers "what commits shipped in this?".
const historyMarker = ".history"

// loadHistory resolves where this controller's program history comes from:
// the archive entry a `nautilus build` embedded, or a live git capture of
// the checkout `nautilus run` was pointed at. The work happens once, on
// first use — GET /api/program/history triggers it, startup never waits.
func loadHistory(fsys fs.FS, dir string) func() *server.ProgramHistory {
	return sync.OnceValue(func() *server.ProgramHistory {
		if raw, err := fs.ReadFile(fsys, historyMarker); err == nil {
			var h server.ProgramHistory
			if json.Unmarshal(raw, &h) == nil {
				return &h
			}
			return nil
		}
		if dir == "" {
			return nil
		}
		h, err := vcs.Capture(dir, vcs.DefaultDepth)
		if err != nil {
			fmt.Fprintln(os.Stderr, "program history:", err, "(continuing without it)")
			return nil
		}
		return h // nil when dir isn't a git checkout — an empty history
	})
}

// manifestFlagUsage documents -m on every command that loads a project.
// One project, one set of programs, several manifests: a site differs by
// which tag files it composes and which controller it points at, and each
// manifest is committed, so what deploys is always something you can read.
const manifestFlagUsage = "manifest to load, relative to the project " +
	"(default nautilus.yaml); e.g. sites/hayward.yaml"

// ── build: emit a deployable binary ─────────────────────────────────────────
// The output is THIS executable with the project zipped onto its tail and a
// 16-byte footer (offset + magic). On start, main() checks the footer and
// becomes the controller instead of the CLI — one file to ship, like any
// compiled program. NAUTILUS_CLI=1 forces CLI behavior on a built binary.

var embedMagic = [8]byte{'N', 'A', 'U', 'T', 'P', 'R', 'O', 'J'}

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	out := fs.String("o", "", "output binary name (default: the manifest's name)")
	manifest := fs.String("m", "", manifestFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	// The project must load AND compile before it ships.
	proj, err := project.Load(os.DirFS(dir), *manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus build:", err)
		return 1
	}
	if _, err := runtime.New(proj.Runtime); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus build: compile:", err)
		return 1
	}
	if proj.HMIDir != "" {
		if st, err := os.Stat(filepath.Join(dir, proj.HMIDir)); err != nil || !st.IsDir() {
			fmt.Fprintf(os.Stderr, "nautilus build: server.hmi: %s: not a directory (run the HMI's own build first, e.g. `npm run build` in its project)\n", proj.HMIDir)
			return 1
		}
	}

	name := *out
	if name == "" {
		name = proj.Name
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus build:", err)
		return 1
	}
	// Provenance rides the artifact: capture the project's git history here,
	// where .git exists, so the deployed controller can answer for its own
	// commits from an air-gapped network. No repo is fine — the endpoint
	// just serves an empty history.
	history, note := captureHistory(dir)
	archiveBytes, err := emitBinary(self, dir, name, *manifest, history)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus build:", err)
		return 1
	}
	fmt.Printf("built %s — a self-contained controller (run it like any binary; NAUTILUS_ADDR/NAUTILUS_TOKEN apply)%s\n", name, note)
	if archiveBytes > embedSizeWarning {
		fmt.Fprintf(os.Stderr,
			"nautilus build: warning: the embedded project is %.1f MB (over the %d MB comfort threshold) — "+
				"if server.hmi is set, check its build didn't drag in a dependency tree (node_modules etc. "+
				"should already be excluded; a stray copy elsewhere in the project would not be)\n",
			float64(archiveBytes)/(1024*1024), embedSizeWarning/(1024*1024))
	}
	return 0
}

// embedSizeWarning is where `nautilus build` starts saying something: a
// controller binary is meant to be "one file to ship," and an HMI's own
// build output is normally a few MB of hashed JS/CSS — comfortably under
// this. Not a hard limit (the archive still embeds and the binary still
// runs), just a nudge to look at what's riding along.
const embedSizeWarning = 50 * 1024 * 1024

// captureHistory snapshots dir's git history as the JSON `.history` embeds,
// plus a note for the build line. Failures degrade to no history — a build
// must not fail because provenance couldn't be read.
func captureHistory(dir string) ([]byte, string) {
	h, err := vcs.Capture(dir, vcs.DefaultDepth)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus build: program history:", err, "(continuing without it)")
		return nil, ""
	}
	if h == nil {
		return nil, " — no git history embedded (not a repo)"
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return nil, ""
	}
	note := fmt.Sprintf(" — %d commits of program history embedded", len(h.Commits))
	if h.BuiltDirty {
		note += " (working tree dirty)"
	}
	return raw, note
}

// manifestMarker records which manifest a built binary loads, for projects
// holding more than one (`nautilus build -m sites/hayward.yaml`). Every
// manifest ships in the archive — the marker only says which one boots, so
// the binary still carries the whole reviewable project. A dot-name because
// the archive walk skips dotfiles: nothing in the project can collide with
// it, and `unzip -p <binary> .manifest` answers "what is this built from?".
const manifestMarker = ".manifest"

// embeddedManifest reads the marker, or "" for a project built without -m
// (which means nautilus.yaml, the default everywhere else).
func embeddedManifest(fsys fs.FS) string {
	raw, err := fs.ReadFile(fsys, manifestMarker)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// embedSkipDirs names project subdirectories that never ship, however large
// they are on disk: dependency trees a package manager can regenerate, not
// project sources. This matters more once an hmi/ directory can sit inside
// the project (server.hmi points at its *build* output, e.g. hmi/build) —
// without this, a `nautilus build` run from beside an un-.gitignore'd
// node_modules would embed the whole SvelteKit toolchain into the CLI
// binary instead of the few static files server.hmi actually needs. Named
// the same as runCheck's own source walk (cmd/nautilus/check.go), for the
// same reason: neither is a project source.
var embedSkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// emitBinary appends dir's project (plus manifest/history markers) onto a
// copy of the runner at self, writing the result to out. It returns the
// archive's uncompressed byte total — runBuild's size warning threshold —
// alongside the usual error.
func emitBinary(self, dir, out, manifest string, history []byte) (int64, error) {
	src, err := os.Open(self)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// 1. the runner: this executable, byte for byte (minus any project a
	//    previous build appended to it).
	base, err := runnerSize(src)
	if err != nil {
		return 0, err
	}
	if _, err := io.CopyN(f, src, base); err != nil {
		return 0, err
	}
	// 2. the project archive.
	zipStart := base
	var archiveBytes int64
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if embedSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Ship the sources, not the artifacts: skip dotdirs, prior builds,
		// and the acceptance suites — tests gate the deploy, they do not
		// ride along on the controller.
		if strings.HasPrefix(rel, ".") || rel == out || rel == filepath.Base(out) ||
			strings.HasSuffix(rel, acceptance.SuffixTest) {
			return nil
		}
		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		archiveBytes += int64(len(raw))
		_, err = w.Write(raw)
		return err
	})
	if err != nil {
		return 0, err
	}
	if manifest != "" {
		w, err := zw.Create(manifestMarker)
		if err != nil {
			return 0, err
		}
		if _, err := w.Write([]byte(manifest)); err != nil {
			return 0, err
		}
	}
	if len(history) > 0 {
		w, err := zw.Create(historyMarker)
		if err != nil {
			return 0, err
		}
		if _, err := w.Write(history); err != nil {
			return 0, err
		}
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	// 3. the footer: where the zip starts, and the magic that marks a
	//    built controller.
	var footer [16]byte
	binary.LittleEndian.PutUint64(footer[:8], uint64(zipStart))
	copy(footer[8:], embedMagic[:])
	_, err = f.Write(footer[:])
	return archiveBytes, err
}

// runnerSize is where the executable's own bytes end: the whole file for a
// plain CLI, or the embedded zip's start when building FROM a built binary.
func runnerSize(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if off, ok := embeddedOffset(f, st.Size()); ok {
		return off, nil
	}
	return st.Size(), nil
}

func embeddedOffset(r io.ReaderAt, size int64) (int64, bool) {
	if size < 16 {
		return 0, false
	}
	var footer [16]byte
	if _, err := r.ReadAt(footer[:], size-16); err != nil {
		return 0, false
	}
	if [8]byte(footer[8:16]) != embedMagic {
		return 0, false
	}
	off := int64(binary.LittleEndian.Uint64(footer[:8]))
	if off < 0 || off >= size-16 {
		return 0, false
	}
	return off, true
}

// embeddedProject opens the project a `nautilus build` appended to this
// executable, if any.
func embeddedProject() (fs.FS, bool) {
	if os.Getenv("NAUTILUS_CLI") == "1" {
		return nil, false
	}
	self, err := os.Executable()
	if err != nil {
		return nil, false
	}
	f, err := os.Open(self)
	if err != nil {
		return nil, false
	}
	st, err := f.Stat()
	if err != nil {
		return nil, false
	}
	off, ok := embeddedOffset(f, st.Size())
	if !ok {
		return nil, false
	}
	zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
	if err != nil {
		return nil, false
	}
	// f stays open for the process lifetime — the zip reads from it lazily.
	return zr, true
}
