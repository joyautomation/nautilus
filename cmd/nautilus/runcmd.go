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

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// runProject hosts a loaded project: scan loop + tag API, Ctrl+C to stop.
func runProject(fsys fs.FS, label string) int {
	proj, err := project.Load(fsys)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus run:", err)
		return 1
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus run: compile:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
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
	// Field-driver + Sparkplug status feed GET /api/drivers and the stream.
	sopts.Drivers = proj.DriverStatus(spNode)
	srv := server.New(rt, sopts)
	go srv.Run(ctx)

	addr := proj.Addr
	if env := os.Getenv("NAUTILUS_ADDR"); env != "" {
		addr = env
	}
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
		banner += " — dashboard + tag API on http://" + addr
	}
	if sparkplugUp {
		banner += " — sparkplug up"
	}
	fmt.Println(banner + " — Ctrl+C to stop")
	<-ctx.Done()
	return 0
}

func runRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	return runProject(os.DirFS(dir), "")
}

// ── build: emit a deployable binary ─────────────────────────────────────────
// The output is THIS executable with the project zipped onto its tail and a
// 16-byte footer (offset + magic). On start, main() checks the footer and
// becomes the controller instead of the CLI — one file to ship, like any
// compiled program. NAUTILUS_CLI=1 forces CLI behavior on a built binary.

var embedMagic = [8]byte{'N', 'A', 'U', 'T', 'P', 'R', 'O', 'J'}

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	out := fs.String("o", "", "output binary name (default: the manifest's name)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	// The project must load AND compile before it ships.
	proj, err := project.Load(os.DirFS(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus build:", err)
		return 1
	}
	if _, err := runtime.New(proj.Runtime); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus build: compile:", err)
		return 1
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
	if err := emitBinary(self, dir, name); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus build:", err)
		return 1
	}
	fmt.Printf("built %s — a self-contained controller (run it like any binary; NAUTILUS_ADDR/NAUTILUS_TOKEN apply)\n", name)
	return 0
}

func emitBinary(self, dir, out string) error {
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()
	// 1. the runner: this executable, byte for byte (minus any project a
	//    previous build appended to it).
	base, err := runnerSize(src)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, src, base); err != nil {
		return err
	}
	// 2. the project archive.
	zipStart := base
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
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
		_, err = w.Write(raw)
		return err
	})
	if err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	// 3. the footer: where the zip starts, and the magic that marks a
	//    built controller.
	var footer [16]byte
	binary.LittleEndian.PutUint64(footer[:8], uint64(zipStart))
	copy(footer[8:], embedMagic[:])
	_, err = f.Write(footer[:])
	return err
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
