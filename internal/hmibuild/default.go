package hmibuild

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/mimicdoc"
)

// The kit's own equipment (hmi Mimic.svelte's builtin map): every other
// component a mimic names is the project's, a {Name}.svelte in it.
var builtinEquipment = map[string]bool{
	"Tank": true, "Heater": true, "Pump": true, "Valve": true, "Gauge": true,
	"Sparkline": true, "CommandButton": true, "WriteToggle": true, "OffPage": true,
	"SetpointGroup": true,
}

// skipDir is what a project search never looks in: dot, dependency and
// build directories, and a directory that is a project of its own (its
// own nautilus.yaml).
func skipDir(dir string) bool {
	name := filepath.Base(dir)
	if strings.HasPrefix(name, ".") || name == "node_modules" || name == "build" || name == "dist" {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, "nautilus.yaml"))
	return err == nil
}

// Mimics lists the project's *.mimic.json, slash paths from the project
// root, sorted.
func Mimics(project string) []string {
	var out []string
	_ = filepath.WalkDir(project, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != project && skipDir(p) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".mimic.json") {
			rel, _ := filepath.Rel(project, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

type mimicDoc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Equipment   []struct {
		Component string   `json:"component"`
		Width     *float64 `json:"width"`
		Height    *float64 `json:"height"`
	} `json:"equipment"`
}

// galleryComp is one of the project's components on the components page:
// its source, and the box the first screen to use it gives it.
type galleryComp struct {
	name, src     string
	width, height *float64
}

// defaultApp is the app for a project with mimics and no app of its own:
// a page per mimic (the first at "/"), live and operable, under a bar
// with the project's name and the first screen's description, the pages
// and Diagnostics, the alarms (the banner opens the alarms page) and Ack
// all, the connection and the theme. A click on equipment with a panel
// opens it beside the drawing (the kit's EquipmentPanel), and every
// bound label carries a sparkline.
func defaultApp(project, title string, extra []json.RawMessage) (*appSource, error) {
	mimics := Mimics(project)
	if len(mimics) == 0 {
		return nil, &Error{Messages: []string{"nothing to build: the project has no hmi/ app (src/routes) and no *.mimic.json"}}
	}
	sources := componentSources(project)
	a := &appSource{dir: project, noStatic: true, virtual: map[string]vmod{}}
	a.layouts = []layout{{Component: "nx:default/layout.svelte"}}
	var screens []string
	var problems []string
	description := ""
	used := map[string]bool{}
	takenSlug := map[string]bool{}
	var assets []json.RawMessage
	for i, m := range mimics {
		raw, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(m)))
		if err != nil {
			return nil, err
		}
		// units drawn out, so the components a unit repeats are registered too
		if x, _, err := mimicdoc.ExpandUnits(raw); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", m, err))
			continue
		} else {
			raw = x
		}
		var doc mimicDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			problems = append(problems, fmt.Sprintf("%s: not a mimic: %v", m, err))
			continue
		}
		assets = append(assets, assetsOf(raw)...)
		var imports, registry []string
		seen := map[string]bool{}
		for _, eq := range doc.Equipment {
			c := eq.Component
			if c == "" || builtinEquipment[c] || seen[c] {
				continue
			}
			seen[c] = true
			src, err := pickSource(sources[c], filepath.Dir(filepath.Join(project, filepath.FromSlash(m))), project)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: component %s: %v", m, c, err))
				continue
			}
			id := fmt.Sprintf("C%d", len(imports))
			imports = append(imports, fmt.Sprintf("\timport %s from %s;", id, jsq(src)))
			registry = append(registry, fmt.Sprintf("%s: %s", jsq(c), id))
			used[c] = true
		}
		// the first mimic is "/", the others by their file name
		id := "/"
		if i > 0 {
			slug := slugOf(m)
			if slug == "" || takenSlug[slug] {
				slug = fmt.Sprintf("%s-%d", slug, i)
			}
			takenSlug[slug] = true
			id = "/" + strings.TrimPrefix(slug, "-")
		}
		if i == 0 {
			description = doc.Description
		}
		label := doc.Name
		if label == "" {
			label = strings.TrimSuffix(filepath.Base(m), ".mimic.json")
		}
		screens = append(screens, fmt.Sprintf("{ id: %s, href: %s, label: %s }", jsq(id), jsq(id), jsq(label)))
		pageMod := fmt.Sprintf("nx:default/page%d.svelte", i)
		a.virtual[pageMod] = vmod{kind: component, text: fmt.Sprintf(defaultPage,
			jsq(filepath.Join(project, filepath.FromSlash(m))), strings.Join(imports, "\n"), strings.Join(registry, ", "))}
		r, _ := routeFor(strings.TrimPrefix(id, "/"))
		r.Page = pageMod
		r.Layouts = []int{0}
		a.routes = append(a.routes, r)
	}
	if len(problems) > 0 {
		return nil, &Error{Messages: problems}
	}
	// the pages every project gets, unless a screen has the name
	alarmsHref := "undefined"
	for _, pg := range []struct{ slug, label, text, needs string }{
		{"diagnostics", "Diagnostics", defaultDiagnostics, ""},
		{"parameters", "Parameters", defaultParameters, ""},
		{"log", "Log", defaultLog, ""},
		// Configuration is the engineer's: the link is not shown to a
		// session without the config permission (the controller refuses the
		// rest in any case).
		{"configuration", "Configuration", defaultConfiguration, "config"},
		{"alarms", "", defaultAlarms, ""},
	} {
		if takenSlug[pg.slug] {
			continue
		}
		mod := "nx:default/" + pg.slug + ".svelte"
		a.virtual[mod] = vmod{kind: component, text: pg.text}
		r, _ := routeFor(pg.slug)
		r.Page = mod
		r.Layouts = []int{0}
		a.routes = append(a.routes, r)
		if pg.label != "" {
			needs := ""
			if pg.needs != "" {
				needs = ", needs: " + jsq(pg.needs)
			}
			screens = append(screens, fmt.Sprintf("{ id: %s, href: %s, label: %s%s }", jsq("/"+pg.slug), jsq("/"+pg.slug), jsq(pg.label), needs))
		}
		if pg.slug == "alarms" {
			alarmsHref = jsq("/alarms")
		}
	}
	// an asset page for each piece of equipment that has a panel, and for
	// each descriptor the caller passed (a device row's), unless a screen
	// is named "assets"
	assets = append(assets, extra...)
	if list := dedupeAssets(assets); len(list) > 0 && !takenSlug["assets"] {
		a.virtual["nx:default/assets"] = vmod{kind: plain, text: "export const assets = " + string(list) + ";\n"}
		for _, pg := range []struct{ dir, mod, text string }{
			{"assets", "nx:default/assets-index.svelte", defaultAssetsIndex},
			{"assets/[id]", "nx:default/asset.svelte", defaultAsset},
		} {
			a.virtual[pg.mod] = vmod{kind: component, text: pg.text}
			r, _ := routeFor(pg.dir)
			r.Page = pg.mod
			r.Layouts = []int{0}
			a.routes = append(a.routes, r)
		}
		screens = append(screens, fmt.Sprintf("{ id: %s, href: %s, label: %s }", jsq("/assets"), jsq("/assets"), jsq("Assets")))
	}
	if !takenSlug["components"] {
		a.addComponentsPage(project, []int{0})
	}
	if title == "" {
		title = filepath.Base(project)
	}
	a.generate()
	a.virtual["nx:default/layout.svelte"] = vmod{kind: component, text: fmt.Sprintf(defaultLayout, jsq(title), jsq(description), strings.Join(screens, ", "), alarmsHref)}
	a.virtual["nx:default/client"] = vmod{kind: plain, text: defaultClient}
	a.appHTML = strings.Replace(defaultHTML, "%title%", htmlEscape(title), 1)
	return a, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugOf(mimic string) string {
	s := strings.ToLower(strings.TrimSuffix(filepath.Base(mimic), ".mimic.json"))
	return strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// componentSources maps a component's name to every {Name}.svelte in the
// project.
func componentSources(project string) map[string][]string {
	out := map[string][]string{}
	_ = filepath.WalkDir(project, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != project && skipDir(p) {
				return filepath.SkipDir
			}
			return nil
		}
		if n := d.Name(); strings.HasSuffix(n, ".svelte") && !strings.HasPrefix(n, "+") {
			name := strings.TrimSuffix(n, ".svelte")
			out[name] = append(out[name], p)
		}
		return nil
	})
	return out
}

// pickSource chooses the {Name}.svelte a mimic means: the one beside the
// mimic, else the only one in the project.
func pickSource(paths []string, mimicDir, project string) (string, error) {
	switch len(paths) {
	case 0:
		return "", fmt.Errorf("no such .svelte in the project (the kit's own are %s)", strings.Join(builtinNames(), ", "))
	case 1:
		return paths[0], nil
	}
	for _, p := range paths {
		if filepath.Dir(p) == mimicDir {
			return p, nil
		}
	}
	var rels []string
	for _, p := range paths {
		r, _ := filepath.Rel(project, p)
		rels = append(rels, filepath.ToSlash(r))
	}
	return "", fmt.Errorf("more than one in the project (%s); keep one, or put the one meant beside the mimic", strings.Join(rels, ", "))
}

func builtinNames() []string {
	var out []string
	for n := range builtinEquipment {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

const defaultClient = `import { createRealtimeClient, createAlarmClient } from '@joyautomation/nautilus-hmi';
// One realtime client (and one alarm client) for the app: the layout
// starts them, every page reads them.
export const rt = createRealtimeClient({ url: '/api/stream' });
export const alarms = createAlarmClient(rt);
`

const defaultLayout = `<script>
	import '@joyautomation/nautilus-hmi/theme.css';
	import '@joyautomation/nautilus-hmi/fonts.css';
	import { onMount } from 'svelte';
	import { theme, ConfirmDialog, AlarmBanner, AckButton, StatusPill, ThemeSwitch, AccountChip, ReferralBanner, createAuth } from '@joyautomation/nautilus-hmi';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { rt, alarms } from 'nx:default/client';
	let { children } = $props();
	// Sign-in (#708): who this screen is signed in as, Sign out, the password
	// prompt for an elevated action, a standby's referral to the leader.
	const auth = createAuth();
	onMount(() => {
		theme.init();
		void auth.start();
		rt.start();
		void alarms.start();
		return () => {
			rt.stop();
			alarms.stop();
			auth.stop();
		};
	});
	const title = %s;
	const description = %s;
	const screens = [%s];
	const alarmsHref = %s;
	// a screen with needs: is listed only to a session that holds it
	const visible = $derived(screens.filter((s) => !s.needs || auth.holds(s.needs)));
</script>

<ConfirmDialog />
<ReferralBanner />
<div class="nx-shell">
	<header>
		<div class="nx-title">
			<b>{title}</b>
			{#if description}<span>{description}</span>{/if}
		</div>
		{#if visible.length > 1}
			<nav aria-label="Screens">
				{#each visible as s (s.id)}
					<a href={resolve(s.href) + page.url.search} aria-current={page.route.id === s.id ? 'page' : undefined}>{s.label}</a>
				{/each}
			</nav>
		{/if}
		<div class="nx-status">
			{#if alarms.supported && alarms.summary}
				<AlarmBanner summary={alarms.summary} compact href={alarmsHref ? resolve(alarmsHref) + page.url.search : undefined} />
				<AckButton alarms={alarms.active} ids={['*']} label="Ack all" size="md" onack={(ids, by) => alarms.ack(ids, by)} />
			{/if}
			<StatusPill kind={rt.connected ? 'good' : 'critical'} label={rt.connected ? 'Connected' : 'Offline'} />
			<AccountChip />
			<ThemeSwitch />
		</div>
	</header>
	<main>{@render children()}</main>
</div>

<style>
	:global(html, body) { margin: 0; height: 100%%; background: var(--bg); color: var(--ink); font-family: var(--font); }
	.nx-shell { display: flex; flex-direction: column; height: 100vh; height: 100dvh; }
	/* one row: the project left, the screens in the middle, the status
	   right; the description gives way first */
	header { display: flex; align-items: center; flex-wrap: wrap; gap: 10px 20px; padding: 10px 16px; font-size: 1rem; border-bottom: 1px solid var(--border, color-mix(in srgb, var(--ink) 14%%, transparent)); background: var(--surface, var(--bg)); }
	.nx-title { flex: 1 1 0; display: flex; align-items: baseline; gap: 10px; min-width: 0; }
	.nx-title b { font-family: var(--font-display, var(--font)); font-size: 1.125rem; white-space: nowrap; }
	.nx-title span { font-size: 1rem; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
	nav { flex: none; display: flex; gap: 2px; padding: 2px; border: 1px solid var(--border, color-mix(in srgb, var(--ink) 14%%, transparent)); border-radius: 8px; background: var(--surface-2, transparent); }
	nav a { color: var(--ink-2, var(--muted)); text-decoration: none; padding: 4px 12px; border-radius: 6px; font-size: 1rem; font-weight: 600; }
	nav a:hover { color: var(--ink); }
	nav a[aria-current='page'] { color: var(--ink); background: color-mix(in srgb, var(--ink) 12%%, transparent); }
	.nx-status { flex: 1 1 0; min-width: max-content; display: flex; align-items: center; justify-content: flex-end; gap: 10px; }
	main { flex: 1; min-height: 0; overflow: auto; padding: 16px; position: relative; }
	@media (max-width: 900px) { .nx-title span { display: none; } }
	/* a phone: the status row (max-content) would be wider than the window
	   and clip; give it a row of its own and let it wrap */
	@media (max-width: 600px) {
		header { padding: 8px 12px; }
		.nx-title { flex-basis: 100%%; }
		nav { flex: 1 1 auto; }
		nav a { flex: 1; text-align: center; }
		.nx-status { flex: 1 1 100%%; min-width: 0; flex-wrap: wrap; justify-content: flex-start; }
	}
</style>
`

// defaultDiagnostics is the Diagnostics page: the controller's scan timing
// and its field drivers.
const defaultDiagnostics = `<script>
	import { ScanDiagnostics, DriverStatusPanel, PushCard } from '@joyautomation/nautilus-hmi';
	import { rt } from 'nx:default/client';
</script>

<div class="nx-page">
	<section>
		<h2>Scan diagnostics</h2>
		{#if rt.frame?.scan}<ScanDiagnostics scan={rt.frame.scan} />{:else}<p>Waiting for the controller…</p>{/if}
	</section>
	<section>
		<h2>Field drivers</h2>
		<DriverStatusPanel drivers={rt.frame?.drivers ?? []} />
	</section>
	<section>
		<h2>This device</h2>
		<PushCard />
	</section>
</div>

<style>
	.nx-page { max-width: 1100px; margin: 0 auto; display: grid; gap: 16px; }
	section { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius, 10px); padding: 16px; }
	h2 { font-size: 0.78rem; font-weight: 700; letter-spacing: 0.07em; text-transform: uppercase; color: var(--muted); margin: 0 0 0.7rem; }
	p { color: var(--muted); margin: 0; }
</style>
`

// defaultParameters is the setpoint parameters (#718): what the plant runs
// against what the manifest's init: says. Read-only here except "Use repo
// value", which asks for the password like any elevated action.
const defaultParameters = `<script>
	import { ParameterList } from '@joyautomation/nautilus-hmi';
</script>

<div class="nx-page">
	<section>
		<h2>Parameters</h2>
		<ParameterList />
	</section>
</div>

<style>
	.nx-page { max-width: 1100px; margin: 0 auto; display: grid; gap: 16px; }
	section { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius, 10px); padding: 16px; }
	h2 { font-size: 0.78rem; font-weight: 700; letter-spacing: 0.07em; text-transform: uppercase; color: var(--muted); margin: 0 0 0.7rem; }
</style>
`

// defaultConfiguration is the drivers and devices as forms (#711), for a
// session with the config permission. The editor stages and applies through
// the controller's own checks; this page only mounts it.
const defaultConfiguration = `<script>
	import { currentAuth } from '@joyautomation/nautilus-hmi';
	import { ConfigEditor } from '@joyautomation/nautilus-hmi/config';
	const auth = $derived(currentAuth());
	const allowed = $derived(!auth || auth.holds('config'));
</script>

<div class="nx-page">
	<section>
		<h2>Configuration</h2>
		{#if allowed}
			<ConfigEditor />
		{:else}
			<p>This page needs the config permission. Sign in as an engineer.</p>
		{/if}
	</section>
</div>

<style>
	.nx-page { max-width: 1100px; margin: 0 auto; display: grid; gap: 16px; }
	section { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius, 10px); padding: 16px; }
	h2 { font-size: 0.78rem; font-weight: 700; letter-spacing: 0.07em; text-transform: uppercase; color: var(--muted); margin: 0 0 0.7rem; }
	p { color: var(--muted); margin: 0; }
</style>
`

// defaultLog is the shift log (#720): the notes for whoever works next,
// written and read here. A controller with no auth: users keeps none and
// the page says so.
const defaultLog = `<script>
	import { ShiftLog, NotesIndex } from '@joyautomation/nautilus-hmi';
</script>

<div class="nx-page">
	<section>
		<ShiftLog />
	</section>
	<section>
		<h2>Notes on equipment, tags and alarms</h2>
		<NotesIndex />
	</section>
</div>

<style>
	.nx-page { max-width: 900px; margin: 0 auto; display: grid; gap: 16px; }
	section { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius, 10px); padding: 16px; }
	h2 { font-size: 0.78rem; font-weight: 700; letter-spacing: 0.07em; text-transform: uppercase; color: var(--muted); margin: 0 0 0.7rem; }
</style>
`

// defaultAlarms is the alarms page the bar's alarm banner opens: the
// active ones, to acknowledge and shelve.
const defaultAlarms = `<script>
	import { AlarmTable, OutOfServicePanel } from '@joyautomation/nautilus-hmi';
	import { alarms } from 'nx:default/client';
	let now = $state(Date.now());
	$effect(() => {
		const id = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(id);
	});
</script>

<div class="nx-page">
	<h1>Alarms</h1>
	{#if !alarms.supported}
		<p>The project defines no alarms (nautilus.yaml's alarms:).</p>
	{:else}
		{#if alarms.error}<p role="alert">{alarms.error}</p>{/if}
		<AlarmTable
			alarms={alarms.instances}
			{now}
			onack={(ids, by) => alarms.ack(ids, by)}
			onshelve={(id, until, by) => alarms.shelve(id, Math.max(1, Math.round((until - Date.now()) / 1000)), by)}
			onunshelve={(id, by) => alarms.unshelve(id, by)}
		/>
		<OutOfServicePanel
			alarms={alarms.instances}
			equipmentList={alarms.equipment}
			windows={alarms.outOfService}
			{now}
			ontake={(equipment, seconds, reason, by) => alarms.takeOutOfService(equipment, seconds, reason, by)}
			onreturn={(equipment, by) => alarms.returnToService(equipment, by)}
		/>
	{/if}
</div>

<style>
	.nx-page { max-width: 1400px; margin: 0 auto; display: grid; gap: 12px; }
	h1 { margin: 0; font-size: var(--font-lg); }
	p { color: var(--muted); margin: 0; }
</style>
`

const defaultPage = `<script>
	import { Mimic, EquipmentPanel, expandUnits, panelFor } from '@joyautomation/nautilus-hmi';
	import doc from %s;
%s
	import { rt } from 'nx:default/client';
	const tags = $derived(rt.frame?.tags ?? {});
	const write = (name, value) => rt.writeTag(name, value);
	// a click on equipment with a panel opens it beside the drawing
	const full = expandUnits(doc);
	const hasPanel = (eq) => !!panelFor(full, eq.id);
	let selected = $state(null);
	const open = $derived(!!panelFor(full, selected));
</script>

<div class="nx-stage" class:nx-with-panel={open}>
	<Mimic {doc} {tags} registry={{ %s }} {write} fit="contain" trends onequipmentclick={(eq) => (selected = eq.id)} clickable={hasPanel} {selected} />
</div>
<EquipmentPanel {doc} {tags} {write} bind:selected />

<style>
	/* the drawing as large as the page allows, moving over for the panel */
	.nx-stage { position: absolute; inset: 0; padding: 16px; box-sizing: border-box; display: flex; flex-direction: column; }
	.nx-stage.nx-with-panel { padding-right: 388px; }
	@media (prefers-reduced-motion: no-preference) { .nx-stage { transition: padding-right 260ms cubic-bezier(0.33, 1, 0.68, 1); } }
	@media (max-width: 900px) { .nx-stage.nx-with-panel { padding-right: 16px; } }
</style>
`

const defaultHTML = `<!doctype html>
<html lang="en">
	<head>
		<meta charset="utf-8" />
		<meta name="viewport" content="width=device-width, initial-scale=1" />
		<link rel="icon" href="data:," />
		<title>%title%</title>
		<script>
			try { var t = localStorage.getItem('theme'); if (t === 'system') t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'; document.documentElement.dataset.theme = t === 'light' ? 'light' : 'dark'; } catch (e) { document.documentElement.dataset.theme = 'dark'; }
		</script>
		%sveltekit.head%
	</head>
	<body>
		%sveltekit.body%
	</body>
</html>
`

// projectGallery is every component of the project's own that a screen
// shows: each a mimic places (with the box the first to place it gives
// it), then each with a {Name}.component.json, as an app's are.
func projectGallery(project string) []*galleryComp {
	sources := componentSources(project)
	var out []*galleryComp
	at := map[string]*galleryComp{}
	for _, m := range Mimics(project) {
		raw, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(m)))
		if err != nil {
			continue
		}
		if x, _, err := mimicdoc.ExpandUnits(raw); err == nil {
			raw = x
		}
		var doc mimicDoc
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		for _, eq := range doc.Equipment {
			c := eq.Component
			if c == "" || builtinEquipment[c] {
				continue
			}
			if g := at[c]; g != nil {
				if g.width == nil && eq.Width != nil {
					g.width = eq.Width
				}
				if g.height == nil && eq.Height != nil {
					g.height = eq.Height
				}
				continue
			}
			src, err := pickSource(sources[c], filepath.Dir(filepath.Join(project, filepath.FromSlash(m))), project)
			if err != nil {
				continue
			}
			g := &galleryComp{name: c, src: src, width: eq.Width, height: eq.Height}
			at[c] = g
			out = append(out, g)
		}
	}
	sidecars := sidecarFiles(project)
	var declared []string
	for name := range sidecars {
		if at[name] == nil && !builtinEquipment[name] && len(sources[name]) > 0 {
			declared = append(declared, name)
		}
	}
	sort.Strings(declared)
	for _, name := range declared {
		src := sources[name][0]
		for _, p := range sources[name][1:] {
			if len(p) < len(src) {
				src = p
			}
		}
		out = append(out, &galleryComp{name: name, src: src})
	}
	return out
}

// addComponentsPage adds ComponentsRoute to the app, under the layouts
// given, when the project has components of its own to show and the app
// has no page there already.
func (a *appSource) addComponentsPage(project string, layouts []int) {
	for _, r := range a.routes {
		if r.ID == ComponentsRoute {
			return
		}
	}
	gallery := projectGallery(project)
	if len(gallery) == 0 {
		return
	}
	const pageMod = "nx:components.svelte"
	if a.virtual == nil {
		a.virtual = map[string]vmod{}
	}
	a.virtual[pageMod] = vmod{kind: component, text: componentsPage(gallery, sidecarFiles(project))}
	r, _ := routeFor(ComponentsRoute[1:])
	r.Page = pageMod
	r.Layouts = layouts
	a.routes = append(a.routes, r)
	a.generate()
}

// ComponentsRoute is where naut's own screens show the project's
// components, each in each of the examples its {Name}.component.json
// gives (`"examples": {"open": {...props}, "closed": {...}}`).
const ComponentsRoute = "/components"

// componentsPage is the Svelte page for ComponentsRoute.
func componentsPage(gallery []*galleryComp, sidecars map[string]json.RawMessage) string {
	var imports, entries []string
	for i, g := range gallery {
		imports = append(imports, fmt.Sprintf("\timport C%d from %s;", i, jsq(g.src)))
		// only what the page draws from: the examples, or a preview
		sc := json.RawMessage("null")
		var all map[string]json.RawMessage
		if json.Unmarshal(sidecars[g.name], &all) == nil {
			keep := map[string]json.RawMessage{}
			for _, k := range []string{"examples", "preview"} {
				if v, ok := all[k]; ok {
					keep[k] = v
				}
			}
			if b, err := json.Marshal(keep); err == nil {
				sc = b
			}
		}
		num := func(f *float64) string {
			if f == nil {
				return "undefined"
			}
			return fmt.Sprint(*f)
		}
		entries = append(entries, fmt.Sprintf("\t\t{ name: %s, C: C%d, width: %s, height: %s, sidecar: %s }",
			jsq(g.name), i, num(g.width), num(g.height), sc))
	}
	return fmt.Sprintf(componentsPageText, strings.Join(imports, "\n"), strings.Join(entries, ",\n"))
}

const componentsPageText = `<script>
%s
	// Each component's examples: its sidecar's "examples" (name -> props),
	// else its "preview" (flat, or {props}), else none. A figure is the size
	// the example gives (its width/height props), else the size a screen
	// first places the component at.
	const comps = [
%s
	];
	const examples = (sc) => {
		if (sc && sc.examples && typeof sc.examples === 'object' && Object.keys(sc.examples).length > 0) return Object.entries(sc.examples);
		if (sc && sc.preview && typeof sc.preview === 'object') return [['preview', sc.preview.props && typeof sc.preview.props === 'object' ? sc.preview.props : sc.preview]];
		return [];
	};
	const write = async () => 'The components page writes nothing';
</script>

<div class="nx-components">
	<h1>Components</h1>
	<p class="nx-lede">Each of the project's own components, in each of the examples its <code>{'{Name}'}.component.json</code> gives.</p>
	{#each comps as c (c.name)}
		{@const C = c.C}
		{@const ex = examples(c.sidecar)}
		<section aria-label={c.name}>
			<h2>{c.name}</h2>
			{#if ex.length === 0}
				<p class="nx-none">No examples: add <code>"examples": {"{"} "running": {"{"} ... props {"}"} {"}"}</code> to {c.name}.component.json. Drawn with no props.</p>
			{/if}
			<div class="nx-row">
				{#each ex.length > 0 ? ex : [['no props', {}]] as [label, props] (label)}
					{@const w = typeof props.width === 'number' ? props.width : c.width}
					{@const h = typeof props.height === 'number' ? props.height : c.height}
					<figure>
						<div class="nx-box" style:width={w ? w + 'px' : undefined} style:height={h ? h + 'px' : undefined}>
							<C look="isa101" {...(c.width ? { width: c.width } : {})} {...(c.height ? { height: c.height } : {})} {...props} {write} />
						</div>
						<figcaption>{label}</figcaption>
					</figure>
				{/each}
			</div>
		</section>
	{/each}
</div>

<style>
	.nx-components { display: grid; gap: 28px; max-width: 1800px; }
	h1 { margin: 0; font-size: var(--font-lg); }
	h2 { margin: 0 0 10px; font-size: var(--font-md); }
	.nx-lede, .nx-none { margin: 0; color: var(--muted); }
	.nx-none { margin-bottom: 10px; }
	code { font-family: var(--mono); }
	.nx-row { display: flex; flex-wrap: wrap; gap: 20px; align-items: flex-start; }
	figure { margin: 0; display: grid; gap: 8px; justify-items: start; }
	.nx-box { position: relative; }
	figcaption { color: var(--ink-2); font-size: var(--font-sm); }
</style>
`

// assetsOf is the asset descriptors a mimic's equipment make: each piece
// with a panel (its own, or the one it shares, followed once) is an asset
// of the same id, titled as its panel is.
func assetsOf(raw []byte) []json.RawMessage {
	var doc struct {
		Equipment []struct {
			ID    string          `json:"id"`
			Label string          `json:"label"`
			Panel json.RawMessage `json:"panel"`
		} `json:"equipment"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	panels := map[string]json.RawMessage{}
	for _, e := range doc.Equipment {
		if len(e.Panel) > 0 && e.Panel[0] == '{' {
			panels[e.ID] = e.Panel
		}
	}
	var out []json.RawMessage
	for _, e := range doc.Equipment {
		panel := e.Panel
		if len(panel) > 0 && panel[0] == '"' {
			var other string
			if json.Unmarshal(panel, &other) != nil {
				continue
			}
			panel = panels[other]
		}
		if len(panel) == 0 || panel[0] != '{' {
			continue
		}
		// titled as the panel is (what a click on it shows), else by its label
		var head struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(panel, &head)
		title := head.Title
		if title == "" {
			title = e.Label
		}
		if title == "" {
			title = e.ID
		}
		d, err := json.Marshal(map[string]any{"id": e.ID, "title": title, "panel": panel})
		if err == nil {
			out = append(out, d)
		}
	}
	return out
}

// assetSlug is the kit's assetSlug (hmi/src/lib/asset.ts): lowercase words
// and digits joined by dashes.
func assetSlug(id string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(id)), "-"), "-")
}

// dedupeAssets is the descriptors as one JSON array, the first of two with
// one page slug kept ("P-1", "P_1" and "p 1" are one page), and any whose id
// makes no slug dropped.
func dedupeAssets(list []json.RawMessage) json.RawMessage {
	seen := map[string]bool{}
	kept := []json.RawMessage{}
	for _, d := range list {
		var h struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(d, &h) != nil {
			continue
		}
		slug := assetSlug(h.ID)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		kept = append(kept, d)
	}
	if len(kept) == 0 {
		return nil
	}
	b, _ := json.Marshal(kept)
	return b
}

// defaultAssetsIndex lists the project's assets, each a link to its page.
const defaultAssetsIndex = `<script>
	import { assetTitle, assetSlug } from '@joyautomation/nautilus-hmi';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { assets } from 'nx:default/assets';
</script>

<div class="nx-page">
	<h1>Assets</h1>
	<ul>
		{#each assets as a (a.id)}
			<li><a href={resolve('/assets/' + assetSlug(a.id)) + page.url.search}>{assetTitle(a)}</a></li>
		{/each}
	</ul>
</div>

<style>
	.nx-page { max-width: 900px; margin: 0 auto; display: grid; gap: 12px; }
	h1 { margin: 0; font-size: var(--font-lg); }
	ul { margin: 0; padding: 0; list-style: none; display: grid; gap: 6px; }
	a { color: var(--ink); }
</style>
`

// defaultAsset is one asset's page: the kit's AssetPage on a descriptor.
const defaultAsset = `<script>
	import { AssetPage, assetSlug } from '@joyautomation/nautilus-hmi';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { assets } from 'nx:default/assets';
	import { rt, alarms } from 'nx:default/client';
	const asset = $derived(assets.find((a) => assetSlug(a.id) === page.params.id));
</script>

{#if asset}
	{#key asset.id}
	<AssetPage {asset} tags={rt.frame?.tags ?? {}} write={(name, value) => rt.writeTag(name, value)} alarms={alarms.supported ? alarms : null} />
	{/key}
{:else}
	<p>No asset by that name. <a href={resolve('/assets') + page.url.search}>All assets</a></p>
{/if}
`
