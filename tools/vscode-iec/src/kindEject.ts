// Eject to component (docs/design/spatial-hmi.md §3g): a kind file's model
// and drives written out as a Svelte component — the scripting rung. The
// drives stay data inside the component (hmi-3d's <ModelNode> applies
// them exactly as the kind editor did), so nothing is lost on the way
// out, and the file is open ground for whatever Threlte can draw beside
// the model. Pure: no vscode import (kindEject.test.ts).

import * as path from "path";
import { formatScene, parseScene } from "./sceneOps";
import type { KindDoc } from "./kindOps";

/** `submersible-pump` → `SubmersiblePump`: the component's file and name. */
export function componentNameFor(kindName: string): string {
  const words = kindName.split(/[^A-Za-z0-9]+/).filter(Boolean);
  const name = words.map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join("");
  return /^[A-Za-z]/.test(name) ? name : "Kind" + name;
}

const TEMPLATE_RE = /\{([A-Za-z_][A-Za-z0-9_]*)(?::\d)?(?:\?[^:}]*:[^}]*)?\}/g;

/** The members a kind reads: `members`, then what its drives and status
 * template name (hmi-3d's kindMembers, internal/scene's EffectiveKinds). */
export function kindMembersOf(kind: KindDoc): string[] {
  const out: string[] = [...(kind.members ?? [])];
  const add = (b: unknown) => {
    if (typeof b !== "string") return;
    const m = b.replace(/^!/, "");
    if (m && !out.includes(m)) out.push(m);
  };
  for (const d of kind.drive ?? []) {
    const body = Object.entries(d).find(([k]) => k !== "mesh")?.[1] as Record<string, unknown> | undefined;
    if (!body) continue;
    const expr = (body.revPerS ?? body.deg ?? body.to ?? body) as Record<string, unknown>;
    add(expr.bind);
  }
  for (const m of (kind.status ?? "").matchAll(TEMPLATE_RE)) add(m[1]);
  return out;
}

export type EjectInput = {
  /** The kind's name (the file's, minus .kind.json). */
  name: string;
  /** The kind file's basename, for the comment. */
  file: string;
  kind: KindDoc;
  /** The model's box as measured on the stage, for a kind with `auto` bounds. */
  measured?: { size: [number, number, number]; center: [number, number, number] };
  date: string;
};

const q = (s: string): string => "'" + s.replace(/\\/g, "\\\\").replace(/'/g, "\\'") + "'";
const IDENT = /^[A-Za-z_$][A-Za-z0-9_$]*$/;

/** A value as a JavaScript literal on one line: `{ at: [0, 1, 0], dir: [0, 1, 0] }`. */
export function lit(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(lit).join(", ")}]`;
  if (v && typeof v === "object") {
    const entries = Object.entries(v as Record<string, unknown>).filter(([, x]) => x !== undefined);
    if (!entries.length) return "{}";
    return `{ ${entries.map(([k, x]) => `${IDENT.test(k) ? k : q(k)}: ${lit(x)}`).join(", ")} }`;
  }
  if (typeof v === "string") return q(v);
  return String(v);
}

/** The component's source. */
export function ejectSource({ name, file, kind, measured, date }: EjectInput): string {
  const comp = componentNameFor(name);
  const members = kindMembersOf(kind);
  const bounds = kind.bounds && kind.bounds !== "auto" ? kind.bounds : measured;
  const meta: string[] = [];
  if (kind.type) meta.push(`\t\ttype: ${q(kind.type)}`);
  if (members.length) meta.push(`\t\tmembers: ${lit(members)}`);
  if (bounds) meta.push(`\t\tbounds: ${lit({ size: bounds.size, center: bounds.center })}`);
  if (kind.labelAt !== undefined) meta.push(`\t\tlabelAt: ${lit(kind.labelAt)}`);
  if (kind.ports && Object.keys(kind.ports).length) meta.push(`\t\tports: ${lit(kind.ports)}`);
  if (kind.status) meta.push(`\t\tstatus: (value, good, over) => formatStatus(${q(kind.status)}, value, good, over)`);
  const drives = (kind.drive ?? []).map((d) => "\t\t" + lit(d));
  const imports = kind.status ? "formatStatus, type KindMeta" : "type KindMeta";
  return [
    `<script lang="ts" module>`,
    `\t// ${comp}: ejected from ${file} on ${date} by the kind editor — the`,
    `\t// model and its drives as code, yours to script. A scene names it as a`,
    `\t// component kind ("${name}": { "component": "…/kinds/${comp}.svelte" });`,
    `\t// the contract below is what naut check holds the scene's nodes to.`,
    `\timport { ${imports} } from '@joyautomation/nautilus-hmi-3d';`,
    ``,
    meta.length ? `\texport const kind: KindMeta = {\n${meta.join(",\n")}\n\t};` : `\texport const kind: KindMeta = {};`,
    `</script>`,
    ``,
    `<script lang="ts">`,
    `\t// <ModelNode> loads the glTF and applies the drives exactly as the kind`,
    `\t// file did. Add anything Threlte beside it — <T.Mesh>, a <Pipe>, a`,
    `\t// useTask of your own — or read the struct yourself (member(value, 'X')).`,
    `\timport { ModelNode, type Drive, type NodeProps } from '@joyautomation/nautilus-hmi-3d';`,
    ``,
    `\tlet { value, good = true, label, selected, ...over }: NodeProps = $props();`,
    ``,
    `\tconst model = ${q(kind.model ?? "")};`,
    drives.length ? `\tconst drive: Drive[] = [\n${drives.join(",\n")}\n\t];` : `\tconst drive: Drive[] = [];`,
    `</script>`,
    ``,
    `<ModelNode data={{ model, drive }} {value} {good} {label} {selected} {...over} />`,
    ``,
  ].join("\n");
}

/** A scene's `kinds` entry that names `kindFile` (a path relative to the
 * scene file), re-pointed at the component: `{ component, type, members }`
 * — what componentRegistry and naut check want of a component kind. The
 * document comes back in the scene editor's canonical form; undefined
 * when the scene does not name the file. */
export function repointScene(sceneText: string, sceneDir: string, kindFile: string, componentFile: string, kind: KindDoc): string | undefined {
  const doc = parseScene(sceneText);
  const kinds = (doc as { kinds?: Record<string, unknown> }).kinds;
  if (!kinds) return undefined;
  let changed = false;
  for (const [name, entry] of Object.entries(kinds)) {
    if (typeof entry !== "string") continue;
    if (path.resolve(sceneDir, entry) !== path.resolve(kindFile)) continue;
    const next: Record<string, unknown> = { component: path.relative(sceneDir, componentFile).split(path.sep).join("/") };
    if (kind.type) next.type = kind.type;
    const members = kindMembersOf(kind);
    if (members.length) next.members = members;
    kinds[name] = next;
    changed = true;
  }
  return changed ? formatScene(doc) : undefined;
}
