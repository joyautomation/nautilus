// Package hmibuild builds an operator screen with no Node and no npm: a
// SvelteKit-style app (src/routes, src/lib, static/, app.html) or, for a
// project with mimics and no app, a default one with a page per mimic.
// esbuild's Go API bundles, Svelte's own compiler runs in goja
// (compiler.go), and a small router follows SvelteKit's file conventions
// on the client (router.go). Go is the only backend: an app is a
// client-only SPA that talks to the controller's tag API, which is how
// every example HMI already ran (ssr = false, adapter-static).
//
// Imports resolve against a closed set of packages naut carries
// (packs.go): Svelte, the HMI kit, the 3D kit, three and Threlte. A
// project's own node_modules is never read.
package hmibuild

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

// Packages is the closed set an HMI may import (and their own subpaths).
var Packages = []string{
	"svelte",
	"@joyautomation/nautilus-hmi",
	"@joyautomation/nautilus-hmi-3d",
	"three",
	"@threlte/core",
	"@threlte/extras",
}

// Options says what to build.
type Options struct {
	// Project is the project root (absolute). The default app finds the
	// project's *.mimic.json and their components under it.
	Project string
	// App is a SvelteKit-style app's directory (absolute), the one with
	// src/routes in it. "" builds the default app from the mimics.
	App string
	// Title names the default app (the project's name).
	Title string
	// Minify the output (on for serving; off reads better when debugging).
	Minify bool
}

// Result is a built app: its files, by slash path from the app's root.
type Result struct {
	Files    map[string][]byte
	Pages    []string // route ids, "/" first
	Warnings []string // "file:line:col: message (code)"
	Took     time.Duration
	Compiled int // Svelte files compiled
}

// FS serves the built files (an fs.FS for server.Options.HMI).
func (r *Result) FS() fs.FS { return mapFS(r.Files) }

// Error is a build that failed: every message, each "file:line:col: text".
type Error struct{ Messages []string }

func (e *Error) Error() string {
	if len(e.Messages) == 1 {
		return e.Messages[0]
	}
	return fmt.Sprintf("%s (and %d more)", e.Messages[0], len(e.Messages)-1)
}

// IsApp says whether dir is a SvelteKit-style app naut can build: it has
// src/routes.
func IsApp(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "src", "routes"))
	return err == nil && st.IsDir()
}

// Build builds the app. A failed build returns *Error.
func Build(o Options) (*Result, error) {
	t0 := time.Now()
	root, err := packRoot()
	if err != nil {
		return nil, err
	}
	comp, err := sharedCompiler(root)
	if err != nil {
		return nil, err
	}
	b := &builder{opts: o, packs: root, comp: comp}
	var app *appSource
	if o.App != "" {
		app, err = scanApp(o.App)
	} else {
		app, err = defaultApp(o.Project, o.Title)
	}
	if err != nil {
		return nil, err
	}
	b.app = app
	if o.Project != "" {
		b.sidecars = sidecarPorts(o.Project)
	} else {
		b.sidecars = sidecarPorts(app.dir)
	}
	res, err := b.bundle()
	if err != nil {
		return nil, err
	}
	res.Took = time.Since(t0)
	return res, nil
}

var (
	compOnce sync.Once
	compVal  *compiler
	compErr  error
)

func sharedCompiler(root string) (*compiler, error) {
	compOnce.Do(func() {
		var script []byte
		if script, compErr = os.ReadFile(filepath.Join(root, "svelte-compiler.js")); compErr == nil {
			compVal, compErr = newCompiler(string(script))
		}
	})
	return compVal, compErr
}

type builder struct {
	opts  Options
	packs string // the extracted pack: node_modules/ and svelte-compiler.js
	comp  *compiler
	app   *appSource
	// the ports each project component's {Name}.component.json declares
	// (sidecar.go), written onto a mimic's equipment as it loads
	sidecars map[string]json.RawMessage

	mu       sync.Mutex
	warnings []string
	compiled int
}

func (b *builder) warn(file string, w Warning) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.warnings = append(b.warnings, fmt.Sprintf("%s:%d:%d: %s (%s)", b.rel(file), w.Line, w.Column, oneLine(w.Message), w.Code))
}

func (b *builder) rel(file string) string {
	base := b.opts.Project
	if base == "" {
		base = b.opts.App
	}
	if r, err := filepath.Rel(base, file); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	if strings.HasPrefix(file, b.packs) {
		return "(naut's packages)/" + strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(file, b.packs)), "/node_modules/")
	}
	return file
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// svelte compiles one file for esbuild; ts strips TypeScript first (a
// .svelte.ts). A fatal warning fails the file.
func (b *builder) svelte(file string, module, ts bool) (string, error) {
	js, err := b.svelteFile(file, module, ts)
	if err != nil {
		// the file's own position, not the import that reached it
		msg := oneLine(err.Error())
		if regexp.MustCompile(`^\d+:\d+: `).MatchString(msg) {
			return "", fmt.Errorf("%s%s:%s", located, b.rel(file), msg)
		}
		return "", fmt.Errorf("%s%s: %s", located, b.rel(file), msg)
	}
	return js, nil
}

// located marks an error text that already starts with its file:line:col.
const located = "\x00"

func (b *builder) svelteFile(file string, module, ts bool) (string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	text := string(raw)
	if ts {
		t := api.Transform(text, api.TransformOptions{Loader: api.LoaderTS, Target: api.ESNext, Sourcefile: file})
		if len(t.Errors) > 0 {
			return "", fmt.Errorf("%s", t.Errors[0].Text)
		}
		text = string(t.Code)
	}
	out, err := b.comp.Compile(text, file, module)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.compiled++
	b.mu.Unlock()
	ours := !strings.HasPrefix(file, b.packs)
	for _, w := range out.Warnings {
		if fatalWarnings[w.Code] {
			return "", fmt.Errorf("%d:%d: %s (%s: it throws when it mounts)", w.Line, w.Column, oneLine(w.Message), w.Code)
		}
		if ours {
			b.warn(file, w)
		}
	}
	return expandGlobs(out.JS, b.app.dir, filepath.Dir(file))
}

func (b *builder) plugin() api.Plugin {
	return api.Plugin{Name: "nautilus-hmi", Setup: func(pb api.PluginBuild) {
		// the generated modules: entry, root, router, $app/*
		pb.OnResolve(api.OnResolveOptions{Filter: `^(nx:|\$app/)`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if _, ok := b.app.virtual[a.Path]; !ok {
				return api.OnResolveResult{}, fmt.Errorf("%s is not a module naut provides (it has $app/state, $app/paths, $app/navigation, $app/environment, $app/stores)", a.Path)
			}
			return api.OnResolveResult{Path: a.Path, Namespace: "nx"}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "nx"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			v := b.app.virtual[a.Path]
			text := v.text
			if v.kind != plain {
				out, err := b.comp.Compile(text, a.Path, v.kind == module)
				if err != nil {
					return api.OnLoadResult{}, fmt.Errorf("naut's generated %s: %v", a.Path, err)
				}
				text = out.JS
			}
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS, ResolveDir: b.app.dir}, nil
		})
		// $lib, as SvelteKit has it
		pb.OnResolve(api.OnResolveOptions{Filter: `^\$lib(/|$)`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			sub := strings.TrimPrefix(strings.TrimPrefix(a.Path, "$lib"), "/")
			r := pb.Resolve("./"+sub, api.ResolveOptions{ResolveDir: filepath.Join(b.app.dir, "src", "lib"), Kind: a.Kind})
			return api.OnResolveResult{Path: r.Path, Errors: r.Errors}, nil
		})
		// every bare import: the closed set, from naut's own packages, one
		// copy each, whoever imports it (a kit's own node_modules included)
		pb.OnResolve(api.OnResolveOptions{Filter: `^[^./$#\x00][^:]*$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if a.PluginData == "pack" {
				return api.OnResolveResult{}, nil
			}
			if !strings.HasPrefix(a.Importer, b.packs) && !allowed(a.Path) {
				return api.OnResolveResult{}, fmt.Errorf("%q is not one of the packages naut builds with (%s); an HMI imports only those, its own files and $lib", a.Path, strings.Join(Packages, ", "))
			}
			r := pb.Resolve(a.Path, api.ResolveOptions{ResolveDir: b.packs, Kind: a.Kind, PluginData: "pack"})
			return api.OnResolveResult{Path: r.Path, Errors: r.Errors, SideEffects: api.SideEffectsTrue}, nil
		})
		// a screen: its components' sidecar ports go onto its equipment
		pb.OnLoad(api.OnLoadOptions{Filter: `\.mimic\.json$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			raw, err := os.ReadFile(a.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			text := string(withSidecarPorts(raw, b.sidecars))
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJSON, ResolveDir: filepath.Dir(a.Path)}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `\.svelte$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			js, err := b.svelte(a.Path, false, false)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			return api.OnLoadResult{Contents: &js, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `\.svelte\.(js|ts)$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			js, err := b.svelte(a.Path, true, strings.HasSuffix(a.Path, ".ts"))
			if err != nil {
				return api.OnLoadResult{}, err
			}
			return api.OnLoadResult{Contents: &js, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
		})
	}}
}

// allowed reports whether a bare specifier names a package of the closed set.
func allowed(spec string) bool {
	name := spec
	if strings.HasPrefix(spec, "@") {
		parts := strings.SplitN(spec, "/", 3)
		if len(parts) >= 2 {
			name = parts[0] + "/" + parts[1]
		}
	} else if i := strings.IndexByte(spec, '/'); i > 0 {
		name = spec[:i]
	}
	for _, p := range Packages {
		if p == name {
			return true
		}
	}
	return false
}

func (b *builder) bundle() (*Result, error) {
	opts := api.BuildOptions{
		EntryPoints:       []string{"nx:entry"},
		Outdir:            "/out",
		EntryNames:        "app",
		ChunkNames:        "_app/[name]-[hash]",
		AssetNames:        "_app/[name]-[hash]",
		Bundle:            true,
		Splitting:         true,
		Format:            api.FormatESModule,
		Platform:          api.PlatformBrowser,
		Target:            api.ES2022,
		Conditions:        []string{"svelte", "browser", "import", "default"},
		MainFields:        []string{"svelte", "browser", "module", "main"},
		ResolveExtensions: []string{".js", ".ts", ".mjs", ".json", ".svelte"},
		Loader: map[string]api.Loader{
			".woff2": api.LoaderFile, ".woff": api.LoaderFile, ".ttf": api.LoaderFile, ".otf": api.LoaderFile,
			".png": api.LoaderFile, ".jpg": api.LoaderFile, ".jpeg": api.LoaderFile, ".gif": api.LoaderFile,
			".webp": api.LoaderFile, ".svg": api.LoaderFile, ".glb": api.LoaderFile, ".gltf": api.LoaderFile,
			".hdr": api.LoaderFile, ".ktx2": api.LoaderFile,
		},
		Define: map[string]string{
			"process.env.NODE_ENV": `"production"`,
			"import.meta.env.DEV":  "false", "import.meta.env.PROD": "true", "import.meta.env.SSR": "false",
			"import.meta.env.MODE": `"production"`,
		},
		Plugins:           []api.Plugin{b.plugin()},
		MinifySyntax:      b.opts.Minify,
		MinifyWhitespace:  b.opts.Minify,
		MinifyIdentifiers: b.opts.Minify,
		LegalComments:     api.LegalCommentsNone,
		Write:             false,
		LogLevel:          api.LogLevelSilent,
		AbsWorkingDir:     b.app.dir,
		// the project's tsconfig is for its own toolchain (it extends
		// .svelte-kit/, which npm makes); naut strips types and nothing else
		TsconfigRaw: "{}",
	}
	r := api.Build(opts)
	if len(r.Errors) > 0 {
		e := &Error{}
		for _, m := range r.Errors {
			e.Messages = append(e.Messages, b.message(m))
		}
		return nil, e
	}
	res := &Result{Files: map[string][]byte{}, Pages: b.app.pageIDs(), Compiled: b.compiled}
	for _, f := range r.OutputFiles {
		rel := strings.TrimPrefix(filepath.ToSlash(f.Path), "/out/")
		res.Files[rel] = f.Contents
	}
	for _, m := range r.Warnings {
		if m.Location != nil && strings.HasPrefix(m.Location.File, b.packs) {
			continue // naut's own packages: not the project's to fix
		}
		res.Warnings = append(res.Warnings, b.message(m))
	}
	res.Warnings = append(res.Warnings, b.warnings...)
	sort.Strings(res.Warnings)
	_, css := res.Files["app.css"]
	res.Files["index.html"] = []byte(b.app.html(css))
	if err := b.app.copyStatic(res.Files); err != nil {
		return nil, err
	}
	return res, nil
}

func (b *builder) message(m api.Message) string {
	if strings.HasPrefix(m.Text, located) {
		return strings.TrimPrefix(m.Text, located)
	}
	text := oneLine(m.Text)
	if m.Location == nil {
		for _, n := range m.Notes {
			if n.Location != nil {
				return fmt.Sprintf("%s:%d:%d: %s", b.rel(n.Location.File), n.Location.Line, n.Location.Column+1, text)
			}
		}
		if m.PluginName != "" && len(m.Notes) > 0 {
			return text + ": " + oneLine(m.Notes[0].Text)
		}
		return text
	}
	file := m.Location.File
	if !filepath.IsAbs(file) && !strings.HasPrefix(file, "nx:") {
		file = filepath.Join(b.app.dir, file)
	}
	// a plugin's error inside a .svelte carries the compiler's own
	// "line:col: " in front; that is the position to report
	if lc := regexp.MustCompile(`^(\d+):(\d+): (.*)$`).FindStringSubmatch(text); lc != nil {
		return fmt.Sprintf("%s:%s:%s: %s", b.rel(file), lc[1], lc[2], lc[3])
	}
	return fmt.Sprintf("%s:%d:%d: %s", b.rel(file), m.Location.Line, m.Location.Column+1, text)
}

// mapFS serves the built files.
func mapFS(files map[string][]byte) fs.FS {
	m := fstest.MapFS{}
	for k, v := range files {
		m[path.Clean(k)] = &fstest.MapFile{Data: v, Mode: 0o444}
	}
	return m
}
