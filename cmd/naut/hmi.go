package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/hmibuild"
	"github.com/joyautomation/nautilus/internal/hmicheck"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// hmiSource says what naut builds for this project, or "" for nothing: the
// app server.hmi names (its src/routes), or the default app from the
// project's mimics.
func hmiSource(proj *project.Project) string {
	switch {
	case proj.HMIApp != "":
		return proj.HMIApp + "/"
	case proj.HMIScreens:
		return "the project's mimics"
	}
	return ""
}

// buildHMI builds the project's HMI with naut's own build (no npm), or
// returns nil when there is nothing for naut to build.
func buildHMI(proj *project.Project, dir string) (*hmibuild.Result, error) {
	if hmiSource(proj) == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	opts := hmibuild.Options{Project: abs, Title: proj.Name, Minify: true}
	if proj.HMIApp != "" {
		opts.App = filepath.Join(abs, filepath.FromSlash(proj.HMIApp))
	}
	return hmibuild.Build(opts)
}

// buildHMIForRun builds the HMI `naut run` serves at "/", or says why it
// can't and leaves the built-in dashboard there. The banner suffix it
// returns names what was built.
func buildHMIForRun(proj *project.Project, dir string, stderr io.Writer) string {
	if dir == "" {
		return ""
	}
	src := hmiSource(proj)
	res, err := buildHMI(proj, dir)
	if err != nil {
		fmt.Fprintf(stderr, "naut run: hmi: %s didn't build, so the built-in dashboard has \"/\":\n", src)
		printBuildError(stderr, "  ", err)
		return ""
	}
	if res == nil {
		return ""
	}
	proj.Server.HMI = res.FS()
	printBuildWarnings(stderr, "naut run: hmi: ", res.Warnings)
	return fmt.Sprintf("hmi (%s, %s, built in %s)", src, plural(len(res.Pages), "page"), res.Took.Round(100*time.Millisecond))
}

func printBuildError(w io.Writer, indent string, err error) {
	var be *hmibuild.Error
	if errors.As(err, &be) {
		for _, m := range be.Messages {
			fmt.Fprintln(w, indent+m)
		}
		return
	}
	fmt.Fprintln(w, indent+err.Error())
}

func printBuildWarnings(w io.Writer, prefix string, warnings []string) {
	const show = 5
	for i, m := range warnings {
		if i == show {
			fmt.Fprintf(w, "%s(and %d more warnings: naut hmi build lists them)\n", prefix, len(warnings)-show)
			return
		}
		fmt.Fprintln(w, prefix+"warning: "+m)
	}
}

const hmiUsage = `usage: naut hmi build [-o dir] [project]

Build the project's operator screen the way naut run serves it, with no
Node and no npm, and say what's wrong with it: the app server.hmi names (a
SvelteKit-style app: src/routes, src/lib, static/), or, with no server.hmi,
a page for each *.mimic.json. Imports resolve against the packages naut
carries: ` + "svelte, @joyautomation/nautilus-hmi, @joyautomation/nautilus-hmi-3d, three, @threlte/core and @threlte/extras" + `.

  -o dir   also write the built files there (for any static host)
`

// runHMI is `naut hmi`.
func runHMI(args []string) int {
	if len(args) == 0 || args[0] != "build" {
		fmt.Fprint(os.Stderr, hmiUsage)
		return 2
	}
	fl := flag.NewFlagSet("naut hmi build", flag.ContinueOnError)
	fl.SetOutput(os.Stderr)
	fl.Usage = func() { fmt.Fprint(os.Stderr, hmiUsage) }
	out := fl.String("o", "", "write the built files to this directory")
	if err := fl.Parse(args[1:]); err != nil {
		return 2
	}
	dir := "."
	if fl.NArg() > 0 {
		dir = fl.Arg(0)
	}
	proj, err := project.Load(os.DirFS(dir), project.ManifestName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut hmi build:", err)
		return 1
	}
	src := hmiSource(proj)
	if src == "" {
		switch {
		case proj.HMIDir != "":
			fmt.Fprintf(os.Stderr, "naut hmi build: server.hmi (%s) is an HMI built elsewhere (no src/routes beside it); naut serves it as it is\n", proj.HMIDir)
		default:
			fmt.Fprintln(os.Stderr, "naut hmi build: nothing to build: no server.hmi app and no *.mimic.json in the project")
		}
		return 1
	}
	res, err := buildHMI(proj, dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut hmi build: %s didn't build:\n", src)
		printBuildError(os.Stderr, "  ", err)
		return 1
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}
	if *out != "" {
		if err := writeBuiltFiles(*out, res.Files); err != nil {
			fmt.Fprintln(os.Stderr, "naut hmi build:", err)
			return 1
		}
	}
	fmt.Printf("built %s: %s (%s), %s, %d Svelte files compiled, in %s\n", src, plural(len(res.Pages), "page"),
		strings.Join(res.Pages, " "), plural(len(res.Warnings), "warning"), res.Compiled, res.Took.Round(100*time.Millisecond))
	return 0
}

func writeBuiltFiles(dir string, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, files[n], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// renderScreens is naut check -render (on unless -render=false).
var renderScreens = true

// renderHMI serves the built screen from naut's own server over a runtime
// that never scans (no driver, no write reaches anything; every tag at
// its initial value) and looks at each page in the machine's browser, at
// desktop and phone size. No browser is a note, not a failure.
func renderHMI(file string, proj *project.Project, res *hmibuild.Result) (errs, warns int) {
	path := hmicheck.FindBrowser()
	if path == "" {
		fmt.Printf("%s: hmi: not rendered: no Chrome, Chromium or Edge on this machine (NAUT_BROWSER names one)\n", file)
		return 0, 0
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		return 0, 0 // the program's own errors are reported above
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	srv := server.New(rt, server.Options{HMI: res.FS()})
	go srv.Run(ctx)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Printf("%s: hmi: not rendered: %v\n", file, err)
		return 0, 0
	}
	hs := &http.Server{Handler: srv.Handler()}
	go hs.Serve(ln)
	defer hs.Close()
	t0 := time.Now()
	found, err := hmicheck.Render(ctx, path, "http://"+ln.Addr().String(), res.Pages)
	for _, f := range found {
		if f.Error {
			errs++
			fmt.Printf("%s: error: hmi: %s\n", file, f)
		} else {
			warns++
			fmt.Printf("%s: warning: hmi: %s\n", file, f)
		}
	}
	if err != nil {
		fmt.Printf("%s: hmi: not rendered: %s: %v\n", file, filepath.Base(path), err)
		return errs, warns
	}
	fmt.Printf("%s: hmi: rendered %s at desktop and phone size in %s: %s\n", file, plural(len(res.Pages), "page"),
		time.Since(t0).Round(100*time.Millisecond), plural(errs, "error")+", "+plural(warns, "warning"))
	return errs, warns
}
