// Pure aggregation/dedup logic for *.component.json sidecar files — no
// vscode import, so it's directly unit-testable with `node --test` over the
// compiled JS (mirrors mimicOps.ts vs mimicEditor.ts: the reducer is pure,
// the extension-host wiring — findFiles, watchers, WorkspaceEdit — lives
// next door in mimicComponents.ts).
//
// Convention: a file named `{ComponentName}.component.json` ANYWHERE in the
// workspace declares metadata for every instance of that component, in
// every *.mimic.json in the project (component names are already unique in
// the registry, so this is name-keyed, not path-keyed). Today the only key
// is `ports` — connection points, as `[x, y]` fractions of the rendered
// box — but the shape is deliberately a general per-component metadata
// file, not a ports-only one: the growth point for things like `defaults`
// (props a palette drop starts with), `bindHints` (tag-bindable prop
// suggestions), `palette` (display name/category/default width), or a
// placeholder aspect/natural size for components with no real renderer.
// None of those are implemented yet — only `ports` is read/written — but
// unknown keys always round-trip untouched (parse, patch just `ports`,
// reserialize) so adding one later doesn't require migrating every sidecar.

/** The direction a pipe LEAVES a port. Mirror of hmi/src/lib/mimic.ts's
 * PortDir. */
export type PortDir = "left" | "right" | "up" | "down";

/** A named connection point, as a fraction of the rendered box. Mirror of
 * webview-ui/src/mimic/portsGestures.ts's Port + hmi/src/lib/mimic.ts's
 * MimicPort. The name is how a pipe end anchors to it explicitly
 * (MimicPipe.from/to). `dir` is the direction a pipe leaves the port;
 * absent = inferred from its edge position (hmi's inferPortDir). Names are
 * unique within a port list. */
export type Port = { name: string; x: number; y: number; dir?: PortDir; part?: string };

/** What the component preview pane (componentPreview.ts) renders a
 * component with: sample props, the user-component counterpart of the
 * built-ins' DEMO_PROPS in webview-ui/src/mimic/registry.ts. Edited from
 * the pane's props strip, or by hand — or written by whoever (a person, a
 * script, an AI agent) authored the component, so it arrives with its own
 * preview data. */
export type ComponentPreviewEntry = {
  props?: Record<string, unknown>;
};

export type ComponentManifestEntry = {
  /** Named fractions of the rendered box. Absent = no sidecar override for
   * this component (falls through to the built-in default). */
  ports?: Port[];
  /** Sample props for the preview pane. Absent = rendered with none. */
  preview?: ComponentPreviewEntry;
  // Reserved for future per-component metadata (defaults, bindHints,
  // palette, ...) — not implemented yet. Any such keys, present in a
  // hand-written or future-version file, pass through every read/patch/
  // write cycle untouched; see applyComponentEntryPatch.
  [key: string]: unknown;
};

const DIRS = new Set(["left", "right", "up", "down"]);

/** Shape + uniqueness check shared by the sidecar's strict parse and the
 * mimic editor's setEquipmentPorts op (mimicOps.ts's twin of this). */
export function validatePortList(ports: unknown): ports is Port[] {
  if (!Array.isArray(ports)) return false;
  if (
    !ports.every(
      (p): p is Port =>
        typeof p === "object" &&
        p !== null &&
        typeof (p as Port).name === "string" &&
        (p as Port).name !== "" &&
        typeof (p as Port).x === "number" &&
        isFinite((p as Port).x) &&
        typeof (p as Port).y === "number" &&
        isFinite((p as Port).y) &&
        ((p as Port).dir === undefined || DIRS.has((p as Port).dir as string)) &&
        ((p as Port).part === undefined || (typeof (p as Port).part === "string" && (p as Port).part !== ""))
    )
  ) {
    return false;
  }
  const names = (ports as Port[]).map((p) => p.name);
  return new Set(names).size === names.length;
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Shape check for the sidecar's `preview` key: an object whose `props`,
 * when present, is a plain map (values are whatever the component takes —
 * numbers, strings, booleans, arrays, nested objects — nothing to check
 * beyond "a map"). */
export function validatePreviewEntry(preview: unknown): preview is ComponentPreviewEntry {
  if (!isPlainObject(preview)) return false;
  return preview.props === undefined || isPlainObject(preview.props);
}

/** "src/lib/HeatExchanger.svelte" -> "HeatExchanger"; null for anything
 * that isn't a candidate component source (a SvelteKit `+page.svelte`,
 * anything under src/routes — see isCandidateComponentSveltePath — or a
 * non-.svelte path). The preview pane's "is this file previewable" test. */
export function componentNameFromSveltePath(path: string): string | null {
  if (!path.endsWith(".svelte")) return null;
  if (!isCandidateComponentSveltePath(path)) return null;
  const base = path.split(/[/\\]/).pop() ?? "";
  const name = base.slice(0, -".svelte".length);
  return name === "" ? null : name;
}

/** The resolved components map — same shape the webview has always
 * received as the `mimicManifest` message's `components` field. Only where
 * it comes from changed (many name-keyed sidecars instead of one central
 * file); resolvePorts() (webview-ui/src/mimic/ports.ts) needs no changes. */
export type ComponentsManifest = Record<string, ComponentManifestEntry>;

const COMPONENT_FILE_RE = /^(.+)\.component\.json$/;

/** "Tank.component.json" -> "Tank"; null if the basename doesn't match (an
 * unrelated file, or a name-less ".component.json"). */
export function componentNameFromFilename(basename: string): string | null {
  const m = COMPONENT_FILE_RE.exec(basename);
  return m && m[1] ? m[1] : null;
}

/** True when a discovered `.svelte` path is a real candidate mimic
 * component — never a SvelteKit route/layout special file (`+page.svelte`,
 * `+layout.svelte`, `+error.svelte`, `+page.server.svelte`, ...: any
 * basename starting with `+`) or anything under a `src/routes/` directory
 * (a route's own local helper components, not equipment) — those are
 * framework structure, not something a project would ever place on a
 * mimic. Shared by mimicComponents.ts's discoverSvelteComponentNames, the
 * one workspace-wide bare-`.svelte` discovery behind both the mimic
 * editor's palette and "Edit Component Ports…" (PR #50), so both benefit
 * from the exclusion. `path` is a filesystem path (fsPath), forward- or
 * back-slashed. */
export function isCandidateComponentSveltePath(path: string): boolean {
  const parts = path.split(/[/\\]/).filter((p) => p !== "");
  const basename = parts[parts.length - 1] ?? "";
  if (basename.startsWith("+")) return false;
  for (let i = 0; i + 1 < parts.length; i++) {
    if (parts[i] === "src" && parts[i + 1] === "routes") return false;
  }
  return true;
}

/** Parse one sidecar's text, forgivingly: missing/empty/malformed/
 * non-object all read back as {} — never throws, so one bad file (a
 * hand-edit mid-keystroke) doesn't take down the whole aggregated index.
 * Contrast parseComponentEntryStrict, used by the single-file component
 * editor, which DOES throw so the author sees the mistake. */
export function parseComponentEntry(text: string): ComponentManifestEntry {
  if (text.trim() === "") return {};
  try {
    const raw: unknown = JSON.parse(text);
    if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return {};
    return raw as ComponentManifestEntry;
  } catch {
    return {};
  }
}

/** Same parse, but throws on empty/malformed/non-object text instead of
 * swallowing it — the *.component.json editor surfaces that as an error
 * banner (mirrors mimicOps.ts's parseMimic/mimicEditor.ts's mimicError). */
export function parseComponentEntryStrict(text: string): ComponentManifestEntry {
  if (text.trim() === "") return {};
  const raw: unknown = JSON.parse(text);
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    throw new Error("expected a JSON object");
  }
  const entry = raw as ComponentManifestEntry;
  if (entry.ports !== undefined && !validatePortList(entry.ports)) {
    throw new Error("ports must be a list of uniquely-named {name, x, y} fraction points");
  }
  if (entry.preview !== undefined && !validatePreviewEntry(entry.preview)) {
    throw new Error("preview must be an object whose props (if any) is a map of prop name to value");
  }
  return entry;
}

export function formatComponentEntry(entry: ComponentManifestEntry): string {
  return JSON.stringify(entry, null, 2) + "\n";
}

/** One discovered *.component.json, pre-read. `path` need only be stable
 * and comparable (a filesystem path, a URI string, whatever the caller
 * has) — dedup compares it by length/lexical order, never interprets it. */
export type ComponentFile = { path: string; text: string };

export type AggregateResult = {
  manifest: ComponentsManifest;
  /** Winning file's path per component name — the edit target for that
   * component's shared ports (an existing sidecar always wins over
   * creating a new one). */
  winners: Record<string, string>;
  /** One line per component name with more than one sidecar file, naming
   * every path considered and which one won. */
  warnings: string[];
};

/** Aggregate every discovered *.component.json into the components map the
 * webview expects. Duplicate `{Name}.component.json` files (same component
 * declared in more than one directory) resolve deterministically: shortest
 * path wins, ties break lexically — so re-aggregating the same file set
 * always picks the same winner, and the caller can `console.warn` every
 * entry in `warnings`. */
export function aggregateComponentFiles(files: ComponentFile[]): AggregateResult {
  const byName = new Map<string, ComponentFile[]>();
  for (const f of files) {
    const basename = f.path.split(/[/\\]/).pop() ?? "";
    const name = componentNameFromFilename(basename);
    if (!name) continue;
    const list = byName.get(name) ?? [];
    list.push(f);
    byName.set(name, list);
  }

  const manifest: ComponentsManifest = {};
  const winners: Record<string, string> = {};
  const warnings: string[] = [];
  for (const [name, list] of byName) {
    const sorted = [...list].sort(
      (a, b) => a.path.length - b.path.length || a.path.localeCompare(b.path)
    );
    const winner = sorted[0];
    manifest[name] = parseComponentEntry(winner.text);
    winners[name] = winner.path;
    if (sorted.length > 1) {
      warnings.push(
        `nautilus: multiple ${name}.component.json found (${sorted.map((f) => f.path).join(", ")}) — using ${winner.path}`
      );
    }
  }
  return { manifest, winners, warnings };
}

/** The mimic editor palette's "custom components" section: every component
 * name with a project sidecar (`manifestNames` — this workspace's
 * *.component.json files), referenced by the open doc's equipment
 * (`docNames`), OR discovered as a bare `{Name}.svelte` anywhere in the
 * workspace with no sidecar yet (`svelteNames` —
 * mimicComponents.ts's discoverSvelteComponentNames, the same discovery
 * PR #50 already used for the "Edit Component Ports…" command's
 * undiscovered-component list) — minus the kit's built-ins — sorted,
 * deduped. A component with no sidecar lists with an empty port list (see
 * resolvePorts()/PORTS fallback — the same "no override -> no ports" the
 * runtime <Mimic> falls back to); dropping one onto the canvas never
 * creates a sidecar on its own — only "Edit Component Ports…" does that,
 * on first save. A *.component.json that happens to share a built-in's
 * name (a ports override, see resolvePorts()) stays out of this list;
 * built-ins always render in their own palette section regardless of
 * whether they have a sidecar. */
export function paletteCustomComponents(
  manifestNames: Iterable<string>,
  docNames: Iterable<string>,
  svelteNames: Iterable<string>,
  builtins: ReadonlySet<string>
): string[] {
  const out = new Set<string>();
  for (const n of manifestNames) if (n && !builtins.has(n)) out.add(n);
  for (const n of docNames) if (n && !builtins.has(n)) out.add(n);
  for (const n of svelteNames) if (n && !builtins.has(n)) out.add(n);
  return [...out].sort();
}

/** A per-key patch of a sidecar entry: a value sets that key, `null`
 * deletes it. Keys not named pass through untouched. */
export type ComponentEntryPatch = Record<string, unknown>;

/** Apply a patch to one sidecar's already-parsed entry, key by key — a
 * `null` deletes the key (e.g. `ports: null` reverts every instance to the
 * built-in default), anything else sets it. Any OTHER keys (future
 * per-component metadata: defaults, bindHints, palette, ...) pass through
 * completely untouched — a patch of the named keys, never a full overwrite. */
export function applyComponentEntryPatch(
  entry: ComponentManifestEntry,
  patch: ComponentEntryPatch
): ComponentManifestEntry {
  const next = { ...entry };
  for (const [key, value] of Object.entries(patch)) {
    if (value === null) delete next[key];
    else next[key] = value;
  }
  return next;
}

/** The ports-only form of applyComponentEntryPatch, kept for its callers. */
export function applyComponentPortsEdit(
  entry: ComponentManifestEntry,
  ports: Port[] | null
): ComponentManifestEntry {
  return applyComponentEntryPatch(entry, { ports });
}

/** Patch named keys of a sidecar's CURRENT text (the open buffer's, dirty
 * or not — never a stale disk read). Strict: text that doesn't parse as a
 * sidecar entry refuses with a message instead of being treated as {} — a
 * half-typed hand edit must never be replaced wholesale (that silently
 * threw away everything in the file). Empty text is a fresh entry. Returns
 * the new text, `null` when the result is empty (the caller deletes the
 * file, or no-ops if there wasn't one), or an error. `what` names the edit
 * in the error ("ports on the canvas", "preview props"). */
export function patchComponentEntryText(
  text: string,
  patch: ComponentEntryPatch,
  what = "the sidecar"
): { text: string | null } | { error: string } {
  let entry: ComponentManifestEntry;
  try {
    entry = parseComponentEntryStrict(text);
  } catch (err) {
    const why = err instanceof Error ? err.message : String(err);
    return { error: `the sidecar isn't valid right now (${why}) — fix the JSON before editing ${what}` };
  }
  const next = applyComponentEntryPatch(entry, patch);
  return { text: Object.keys(next).length === 0 ? null : formatComponentEntry(next) };
}

/** Patch just the `ports` key — see patchComponentEntryText. */
export function patchComponentPortsText(
  text: string,
  ports: Port[] | null
): { text: string | null } | { error: string } {
  return patchComponentEntryText(text, { ports }, "ports on the canvas");
}

/** Patch just the `preview` key: `props` sets the preview's sample props;
 * an empty map drops the whole `preview` key (nothing worth keeping). */
export function patchComponentPreviewText(
  text: string,
  props: Record<string, unknown>
): { text: string | null } | { error: string } {
  const preview = Object.keys(props).length === 0 ? null : { props };
  return patchComponentEntryText(text, { preview }, "preview props");
}
