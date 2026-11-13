package hmibuild

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// Pack writes the pack (packs.tar.gz) from an npm install of the closed
// set in dir (packs/: its package.json pins them). It keeps only the
// files the packages' public entry points reach (following the kits'
// .svelte through Svelte's compiler, as a build does), their package.json
// files, and svelte-compiler.js. Deterministic: the same install packs to
// the same bytes.
func Pack(dir string, w io.Writer) error {
	nm := filepath.Join(dir, "node_modules")
	script, err := bundleCompiler(dir)
	if err != nil {
		return err
	}
	comp, err := newCompiler(script)
	if err != nil {
		return err
	}
	entry, err := kitchenSink(nm)
	if err != nil {
		return err
	}
	b := &builder{packs: dir, comp: comp, app: &appSource{dir: dir, virtual: map[string]vmod{}}}
	plugin := api.Plugin{Name: "nautilus-pack", Setup: func(pb api.PluginBuild) {
		pb.OnResolve(api.OnResolveOptions{Filter: `^nx:sink$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: "sink", Namespace: "nx"}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "nx"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			return api.OnLoadResult{Contents: &entry, Loader: api.LoaderJS, ResolveDir: dir}, nil
		})
		pb.OnResolve(api.OnResolveOptions{Filter: `^[^./#\x00][^:]*$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if a.PluginData == "pack" {
				return api.OnResolveResult{}, nil
			}
			r := pb.Resolve(a.Path, api.ResolveOptions{ResolveDir: dir, Kind: a.Kind, PluginData: "pack"})
			return api.OnResolveResult{Path: r.Path, Errors: r.Errors, SideEffects: api.SideEffectsTrue}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `\.svelte$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			js, err := b.svelte(a.Path, false, false)
			return api.OnLoadResult{Contents: &js, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, err
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `\.svelte\.(js|ts)$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			js, err := b.svelte(a.Path, true, strings.HasSuffix(a.Path, ".ts"))
			return api.OnLoadResult{Contents: &js, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, err
		})
	}}
	r := api.Build(api.BuildOptions{
		EntryPoints:   []string{"nx:sink"},
		Outdir:        "/out",
		Bundle:        true,
		Format:        api.FormatESModule,
		Platform:      api.PlatformBrowser,
		Target:        api.ES2022,
		Conditions:    []string{"svelte", "browser", "import", "default"},
		MainFields:    []string{"svelte", "browser", "module", "main"},
		Loader:        map[string]api.Loader{".woff2": api.LoaderFile, ".woff": api.LoaderFile, ".ttf": api.LoaderFile, ".png": api.LoaderFile, ".svg": api.LoaderFile, ".glb": api.LoaderFile},
		Plugins:       []api.Plugin{plugin},
		Metafile:      true,
		Write:         false,
		LogLevel:      api.LogLevelSilent,
		AbsWorkingDir: dir,
	})
	if len(r.Errors) > 0 {
		var msgs []string
		for _, m := range r.Errors {
			msgs = append(msgs, b.message(m))
		}
		return &Error{Messages: msgs}
	}
	var meta struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(r.Metafile), &meta); err != nil {
		return err
	}
	pkgs, err := packageDirs(nm)
	if err != nil {
		return err
	}
	files := map[string]string{} // pack path -> real file
	for in := range meta.Inputs {
		if strings.HasPrefix(in, "nx:") {
			continue
		}
		real := in
		if !filepath.IsAbs(real) {
			real = filepath.Join(dir, in)
		}
		if p, err := filepath.EvalSymlinks(real); err == nil {
			real = p
		}
		name, pkgReal, ok := owner(pkgs, real)
		if !ok {
			return fmt.Errorf("%s is outside the packages the pack holds", in)
		}
		rel, _ := filepath.Rel(pkgReal, real)
		files[filepath.ToSlash(filepath.Join("node_modules", name, rel))] = real
		// every package.json from the file up to its package's own
		for d := filepath.Dir(real); strings.HasPrefix(d, pkgReal); d = filepath.Dir(d) {
			if pj := filepath.Join(d, "package.json"); exists(pj) {
				r, _ := filepath.Rel(pkgReal, pj)
				files[filepath.ToSlash(filepath.Join("node_modules", name, r))] = pj
			}
			if d == pkgReal {
				break
			}
		}
	}
	return writeTGZ(w, files, map[string][]byte{"svelte-compiler.js": []byte(script)})
}

// bundleCompiler bundles svelte/compiler (the pinned Svelte's) into one
// script that defines svelteCompiler, for goja.
func bundleCompiler(dir string) (string, error) {
	r := api.Build(api.BuildOptions{
		EntryPoints:      []string{"svelte/compiler"},
		NodePaths:        []string{filepath.Join(dir, "node_modules")},
		AbsWorkingDir:    dir,
		Bundle:           true,
		Format:           api.FormatIIFE,
		GlobalName:       "svelteCompiler",
		Platform:         api.PlatformBrowser,
		Target:           api.ES2022,
		MinifyWhitespace: true,
		MinifySyntax:     true,
		LegalComments:    api.LegalCommentsNone,
		Banner:           map[string]string{"js": "var process = { env: {}, platform: 'browser', versions: {}, cwd: function () { return '/'; } };"},
		Write:            false,
		LogLevel:         api.LogLevelSilent,
	})
	if len(r.Errors) > 0 {
		return "", fmt.Errorf("bundling svelte/compiler: %s", r.Errors[0].Text)
	}
	return string(r.OutputFiles[0].Contents), nil
}

// kitchenSink is a module that re-exports every public entry point of the
// closed set (and imports their stylesheets), so the build reaches every
// file an HMI could.
func kitchenSink(nm string) (string, error) {
	var lines []string
	n := 0
	for _, pkg := range Packages {
		subs, err := entryPoints(filepath.Join(nm, pkg), pkg != "three")
		if err != nil {
			return "", fmt.Errorf("%s: %w", pkg, err)
		}
		for _, sub := range subs {
			spec := pkg
			if sub != "." {
				spec = pkg + strings.TrimPrefix(sub, ".")
			}
			if strings.HasSuffix(spec, ".css") {
				lines = append(lines, fmt.Sprintf("import %s;", jsq(spec)))
				continue
			}
			lines = append(lines, fmt.Sprintf("export * as __%d from %s;", n, jsq(spec)))
			n++
		}
	}
	// what compiled components import, beyond the public API
	for _, spec := range []string{"svelte/internal/client", "svelte/internal/disclose-version", "svelte/internal/flags/legacy", "svelte/internal/flags/async", "svelte/internal/flags/tracing"} {
		lines = append(lines, fmt.Sprintf("export * as __%d from %s;", n, jsq(spec)))
		n++
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// entryPoints lists a package's export subpaths a browser app can import:
// its exports map (wildcards expanded against the files), or "." alone.
// Wildcard exports expand only with wildcards set: three's ("./addons/*",
// "./examples/jsm/*") would pack every addon; the ones the kits use are
// reached through them anyway.
func entryPoints(pkgDir string, wildcards bool) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return nil, err
	}
	var pj struct {
		Exports json.RawMessage `json:"exports"`
	}
	if err := json.Unmarshal(raw, &pj); err != nil {
		return nil, err
	}
	var exports map[string]json.RawMessage
	if len(pj.Exports) == 0 || json.Unmarshal(pj.Exports, &exports) != nil || len(exports) == 0 || !strings.HasPrefix(firstKey(exports), ".") {
		return []string{"."}, nil
	}
	var out []string
	for key, target := range exports {
		if key == "./package.json" || serverish(key) {
			continue
		}
		t := browserTarget(target)
		if t == "" {
			continue
		}
		if !strings.Contains(key, "*") {
			out = append(out, key)
			continue
		}
		if !wildcards {
			continue
		}
		// "./profiles/*" -> "./dist/profiles/*.js": the files that exist
		matches, _ := filepath.Glob(filepath.Join(pkgDir, filepath.FromSlash(strings.Replace(t, "*", "*", 1))))
		pre, suf, _ := strings.Cut(filepath.FromSlash(t), "*")
		for _, m := range matches {
			if st, err := os.Stat(m); err != nil || st.IsDir() {
				continue
			}
			rel, _ := filepath.Rel(pkgDir, m)
			rel = "./" + filepath.ToSlash(rel)
			star := strings.TrimSuffix(strings.TrimPrefix(rel, filepath.ToSlash(pre)), filepath.ToSlash(suf))
			out = append(out, strings.Replace(key, "*", star, 1))
		}
	}
	sort.Strings(out)
	return out, nil
}

func firstKey(m map[string]json.RawMessage) string {
	for k := range m {
		return k
	}
	return ""
}

func serverish(key string) bool {
	for _, w := range []string{"server", "compiler", "ssr", "node", "/types", ".d.ts", "/register", "legacy"} {
		if strings.Contains(key, w) {
			return true
		}
	}
	return false
}

// browserTarget picks an exports entry's target for a browser build.
func browserTarget(t json.RawMessage) string {
	var s string
	if json.Unmarshal(t, &s) == nil {
		return s
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(t, &m) != nil {
		return ""
	}
	for _, c := range []string{"svelte", "browser", "import", "default"} {
		if v, ok := m[c]; ok {
			if r := browserTarget(v); r != "" {
				return r
			}
		}
	}
	return ""
}

type pkgDir struct{ name, real string }

// packageDirs lists every installed package (scoped and nested ones too)
// with its real directory (a file: link resolved).
func packageDirs(nm string) ([]pkgDir, error) {
	var out []pkgDir
	var walk func(nm, prefix string) error
	walk = func(nm, prefix string) error {
		entries, err := os.ReadDir(nm)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			p := filepath.Join(nm, name)
			if strings.HasPrefix(name, "@") {
				if err := walk(p, prefix+name+"/"); err != nil {
					return err
				}
				continue
			}
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				continue
			}
			out = append(out, pkgDir{name: prefix + name, real: real})
		}
		return nil
	}
	if err := walk(nm, ""); err != nil {
		return nil, err
	}
	// longest first, so a package inside another's directory owns its files
	sort.Slice(out, func(i, j int) bool { return len(out[i].real) > len(out[j].real) })
	return out, nil
}

func owner(pkgs []pkgDir, real string) (name, dir string, ok bool) {
	for _, p := range pkgs {
		if real == p.real || strings.HasPrefix(real, p.real+string(filepath.Separator)) {
			return p.name, p.real, true
		}
	}
	return "", "", false
}

func writeTGZ(w io.Writer, files map[string]string, extra map[string][]byte) error {
	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	var names []string
	for n := range files {
		names = append(names, n)
	}
	for n := range extra {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		data, ok := extra[n]
		if !ok {
			if data, err = os.ReadFile(files[n]); err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}
