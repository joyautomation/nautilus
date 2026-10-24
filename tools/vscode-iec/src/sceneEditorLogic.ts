// The scene editor's pure half (no vscode import, unit-tested with
// node --test like mimicOps.ts): where a scene's asset paths live on disk,
// and the copy of the document the webview renders, with every asset path
// turned into something the webview can fetch.
//
// A *.scene.json names its models, textures and HDRIs as URL paths from the
// APP root (`models/pump.glb`), the way the built HMI serves them. The
// extension has no app root, only files, so it looks where `naut check`
// looks (internal/project/scene.go): next to the scene file, under the HMI
// app's static/, and in its build — and hands the webview a URI for the
// first that exists. A path found nowhere is left as written (the renderer
// falls back to the kind's grey primitive) and reported, so the editor can
// say which file is missing instead of showing a silent hole.

import * as path from "path";

type Json = Record<string, unknown>;
const isObj = (v: unknown): v is Json => typeof v === "object" && v !== null && !Array.isArray(v);

/** The directories, relative to the scene file's, that an asset path is
 * tried against, in order. `hmi/` is the project layout `naut new` makes
 * and every example uses; `static/` is where SvelteKit serves files from
 * in development and `build/` after `npm run build`. */
export const ASSET_ROOTS = [".", "hmi/static", "hmi/build", "static", "build"];

/** Every asset path a document names, as [json-pointer, path] pairs —
 * the TS twin of internal/scene's Doc.Assets(). */
export function sceneAssets(doc: unknown): [string, string][] {
  const out: [string, string][] = [];
  if (!isObj(doc)) return out;
  const kinds = isObj(doc.kinds) ? doc.kinds : {};
  for (const [name, k] of Object.entries(kinds)) {
    if (isObj(k) && typeof k.model === "string" && k.model) out.push([`/kinds/${name}/model`, k.model]);
  }
  const env = isObj(doc.environment) ? doc.environment : {};
  for (const key of ["hdri", "backdrop"]) {
    if (typeof env[key] === "string" && env[key]) out.push([`/environment/${key}`, env[key] as string]);
  }
  const fixtures = Array.isArray(doc.fixtures) ? doc.fixtures : [];
  fixtures.forEach((f, i) => {
    if (!isObj(f) || !isObj(f.texture)) return;
    for (const key of ["map", "normalMap", "roughnessMap"]) {
      const v = f.texture[key];
      if (typeof v === "string" && v) out.push([`/fixtures/${i}/texture/${key}`, v]);
    }
  });
  return out;
}

export type AssetReport = { pointer: string; path: string; file?: string };

/** The document as the webview renders it: a deep copy with every asset
 * path that exists on disk replaced by `toUri(file)`, plus the report of
 * where each landed (file undefined = found nowhere). `exists` and
 * `toUri` are the host's; `sceneDir` is the scene file's directory. */
export function resolveSceneAssets(
  doc: unknown,
  sceneDir: string,
  exists: (file: string) => boolean,
  toUri: (file: string) => string
): { doc: unknown; assets: AssetReport[] } {
  const copy = JSON.parse(JSON.stringify(doc ?? null)) as unknown;
  const assets: AssetReport[] = [];
  for (const [pointer, p] of sceneAssets(copy)) {
    const file = locateAsset(p, sceneDir, exists);
    assets.push(file ? { pointer, path: p, file } : { pointer, path: p });
    if (file) setPointer(copy, pointer, toUri(file));
  }
  return { doc: copy, assets };
}

/** The first root under which `assetPath` exists, as an absolute file
 * path; undefined when none has it. An absolute path, or one that climbs
 * out with `..`, is never resolved — a scene names files the app serves. */
export function locateAsset(assetPath: string, sceneDir: string, exists: (file: string) => boolean): string | undefined {
  // A leading slash is the URL's root (the app root), not the filesystem's.
  const rel = assetPath.replace(/^\.?\//, "");
  if (path.isAbsolute(rel) || rel.split(/[\\/]/).includes("..")) return undefined;
  for (const root of ASSET_ROOTS) {
    const file = path.join(sceneDir, root, rel);
    if (exists(file)) return file;
  }
  return undefined;
}

function setPointer(doc: unknown, pointer: string, value: unknown): void {
  const parts = pointer.split("/").slice(1);
  let cur: unknown = doc;
  for (let i = 0; i < parts.length - 1; i++) {
    const key = parts[i];
    if (Array.isArray(cur)) cur = cur[Number(key)];
    else if (isObj(cur)) cur = cur[key];
    else return;
  }
  const last = parts[parts.length - 1];
  if (Array.isArray(cur)) cur[Number(last)] = value;
  else if (isObj(cur)) cur[last] = value;
}

// ── kind files ─────────────────────────────────────────────────────────────

/** One `kinds` entry that names a kind file: where it was looked for, and
 * what came of it — `file` absent = not found, `error` = found but not a
 * kind. Posted to the webview for its banner and the palette's caption. */
export type KindFileReport = { name: string; path: string; file?: string; error?: string };

/** A kind file path (hmi-3d's isKindPath): relative to the scene file,
 * a `.kind.json`, no scheme, not absolute, no `..`. */
export function isKindPath(p: unknown): p is string {
  return typeof p === "string" && p.endsWith(".kind.json") && !/^[a-z]+:/i.test(p) && !p.startsWith("/") && !p.split(/[\\/]/).includes("..");
}

/** The document with every kind-file entry replaced by the object the
 * file holds — what hmi-3d's inlineKindFiles does from the app's modules,
 * done here from disk, relative to the scene file's directory (the rule a
 * component path follows). An entry whose file is missing or not a kind
 * stays as written and is reported; a `component` or `assembly` in a
 * kind file is refused as everywhere else. */
export function inlineKindFilesOnDisk(
  doc: unknown,
  sceneDir: string,
  exists: (file: string) => boolean,
  read: (file: string) => string
): { doc: unknown; files: KindFileReport[] } {
  const files: KindFileReport[] = [];
  if (!isObj(doc) || !isObj(doc.kinds)) return { doc, files };
  const entries = Object.entries(doc.kinds);
  if (!entries.some(([, k]) => typeof k === "string")) return { doc, files };
  const copy = JSON.parse(JSON.stringify(doc)) as Json;
  const kinds = copy.kinds as Json;
  for (const [name, k] of entries) {
    if (typeof k !== "string") continue;
    if (!isKindPath(k)) {
      files.push({ name, path: k, error: "a kind file is a {Name}.kind.json as a path relative to the scene file" });
      continue;
    }
    const file = path.join(sceneDir, k);
    if (!exists(file)) {
      files.push({ name, path: k });
      continue;
    }
    let obj: unknown;
    try {
      obj = JSON.parse(read(file));
    } catch (err) {
      files.push({ name, path: k, file, error: `not valid JSON: ${err instanceof Error ? err.message : String(err)}` });
      continue;
    }
    if (!isObj(obj)) {
      files.push({ name, path: k, file, error: "a kind file is an object: { model, status, drive, ... }" });
      continue;
    }
    if (obj.component !== undefined || obj.assembly !== undefined) {
      files.push({ name, path: k, file, error: "a kind file is a data kind or a contract — a component or an assembly kind stays inline in the scene" });
      continue;
    }
    kinds[name] = obj;
    files.push({ name, path: k, file });
  }
  return { doc: copy, files };
}
