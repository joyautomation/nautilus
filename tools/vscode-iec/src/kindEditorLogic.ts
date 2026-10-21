// The kind editor's pure half (no vscode import; kindEditorLogic.test.ts):
// what the file is called, and where its model is on disk.
//
// A kind file lives in the app's source tree (`hmi/src/lib/kinds/`) and
// names its model as a URL path from the APP root (`models/pump.glb` →
// `hmi/static/models/pump.glb`), the way the scene does. The scene editor
// resolves that from the scene file's directory (sceneEditorLogic.ts); a
// kind file is deeper, so this walks up from the file's folder, trying the
// same roots at each level, until the first that has it.

import * as path from "path";
import { ASSET_ROOTS, locateAsset, type AssetReport } from "./sceneEditorLogic";

/** `hmi/src/lib/kinds/submersible-pump.kind.json` → `submersible-pump`:
 * the name the palette shows, and the key a scene would use. */
export function kindNameFromFilename(file: string): string {
  const base = path.basename(file);
  return base.endsWith(".kind.json") ? base.slice(0, -".kind.json".length) : base.replace(/\.json$/, "");
}

/** How many folders up from the kind file the roots are tried: enough
 * for `hmi/src/lib/kinds/` to reach the project root, and a level more. */
export const UP_LEVELS = 5;

/** The model's file, walking up from `kindDir`: at each level the scene
 * editor's roots (`.`, `hmi/static`, `hmi/build`, `static`, `build`)
 * are tried, so a kind file next to its model, under `src/lib`, or in a
 * project of its own all resolve. Undefined when nothing has it. */
export function locateKindModel(modelPath: string, kindDir: string, exists: (file: string) => boolean): string | undefined {
  let dir = kindDir;
  for (let i = 0; i <= UP_LEVELS; i++) {
    const hit = locateAsset(modelPath, dir, exists);
    if (hit) return hit;
    const up = path.dirname(dir);
    if (up === dir) break;
    dir = up;
  }
  return undefined;
}

/** The kind as the webview renders it: `model` replaced by a URI the
 * webview can fetch when the file exists, with the report of where it
 * landed (file absent = nowhere; no report when the kind has no model). */
export function resolveKindModel(
  kind: Record<string, unknown>,
  kindDir: string,
  exists: (file: string) => boolean,
  toUri: (file: string) => string
): { kind: Record<string, unknown>; model?: AssetReport } {
  const copy = JSON.parse(JSON.stringify(kind)) as Record<string, unknown>;
  const m = typeof copy.model === "string" && copy.model ? copy.model : undefined;
  if (!m) return { kind: copy };
  const file = locateKindModel(m, kindDir, exists);
  if (file) copy.model = toUri(file);
  return { kind: copy, model: file ? { pointer: "/model", path: m, file } : { pointer: "/model", path: m } };
}

export { ASSET_ROOTS };
