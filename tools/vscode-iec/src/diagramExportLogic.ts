// The decisions behind "Export Diagram…" and "Print Diagram…" that do not
// need VS Code: which diagram a file is, what the exported file is called,
// which paper it prints on. The drawing itself lives in
// webview-ui/src/export (bundled to media/dist/diagram-export.cjs).

import * as fs from "fs";
import * as os from "os";
import * as path from "path";

export type DiagramLang = "ld" | "fbd" | "sfc";
export type ExportFormat = "pdf" | "svg";
export type Paper = "letter" | "a4";
export type Orientation = "auto" | "portrait" | "landscape";

/** The diagram a file holds, by its name; an L5X renders as ladder. */
export function diagramLangOf(file: string | undefined): DiagramLang | undefined {
  if (!file) return undefined;
  const ext = path.extname(file).toLowerCase();
  if (ext === ".ld" || ext === ".l5x") return "ld";
  if (ext === ".fbd") return "fbd";
  if (ext === ".sfc") return "sfc";
  return undefined;
}

/** The diagram a VS Code language id is. */
export function diagramLangOfLanguage(languageId: string): DiagramLang | undefined {
  switch (languageId) {
    case "iec-ld":
    case "logix-l5x":
      return "ld";
    case "iec-fbd":
      return "fbd";
    case "iec-sfc":
      return "sfc";
    default:
      return undefined;
  }
}

/** The extension's diagram webviews, by their real viewType: the "Open
 * With" diagram editors (package.json customEditors) and the "Open …
 * Preview" panels (createWebviewPanel in ld/fbd/sfcPreview.ts). The FBD
 * preview panel's id has no dot (it predates the others' naming) and is
 * kept as it is. diagramExportLogic.test.ts reads these ids back out of
 * the code and package.json, so a renamed view fails the test instead of
 * silently losing its Export/Print menu items. */
export const DIAGRAM_VIEW_TYPES: Readonly<Record<string, DiagramLang>> = {
  "nautilus.ldDiagram": "ld",
  "nautilus.l5xDiagram": "ld",
  "nautilus.fbdDiagram": "fbd",
  "nautilus.sfcDiagram": "sfc",
  "nautilus.ldPreview": "ld",
  nautilusFbdPreview: "fbd",
  "nautilus.sfcPreview": "sfc",
};

/** The diagram behind one of the extension's webview panels — the
 * "Open … Preview" panels and the "Open With" diagram editors. */
export function diagramLangOfView(viewType: string | undefined): DiagramLang | undefined {
  if (!viewType) return undefined;
  // A webview tab's viewType arrives prefixed ("mainThreadWebview-…") on
  // some VS Code versions, so match the end.
  for (const [id, lang] of Object.entries(DIAGRAM_VIEW_TYPES)) {
    if (viewType === id || viewType.endsWith("-" + id)) return lang;
  }
  return undefined;
}

/** `permissives.ld` → `permissives.pdf`, next to the source. An L5X keeps
 * its extension in the name (`Line.L5X.pdf`), since one controller export
 * may sit beside the nautilus source it was generated from. */
export function exportFileName(source: string, format: ExportFormat): string {
  const base = path.basename(source);
  const ext = path.extname(base);
  const stem = ext.toLowerCase() === ".l5x" || ext === "" ? base : base.slice(0, -ext.length);
  return `${stem || "diagram"}.${format}`;
}

// Where Letter is the paper in the drawer: the US, Canada, Mexico and a
// few others; A4 everywhere else.
const LETTER_REGIONS = new Set(["US", "CA", "MX", "PH", "CL", "CO", "VE", "GT", "CR", "PR", "PA", "DO", "SV", "NI", "BZ"]);

/** The paper for a setting: "auto" follows the system locale's region. */
export function paperFor(setting: string | undefined, locale: string | undefined): Paper {
  if (setting === "letter" || setting === "a4") return setting;
  const region = /[-_]([A-Za-z]{2})\b/.exec(locale ?? "")?.[1]?.toUpperCase();
  return region && LETTER_REGIONS.has(region) ? "letter" : "a4";
}

export function orientationFor(setting: string | undefined): Orientation {
  return setting === "portrait" || setting === "landscape" ? setting : "auto";
}

/** A flat name → value map from the live snapshot's tags and program
 * locals, as the diagram views resolve labels against: a tag wins over a
 * local of the same name, as it does in the live stream. */
export function liveValueMap(tags: [string, unknown][], locals: [string, unknown][]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of [...locals, ...tags]) out[k] = v;
  return out;
}

/** Where "Print Diagram…" sends the PDF. A PDF viewer on this machine is
 * out of reach from a remote window (SSH, WSL, Dev Containers,
 * Codespaces): the file would be written on the remote and the viewer, if
 * any, would open there or nowhere. So a remote window saves instead. */
export function printRoute(remoteName: string | undefined): "viewer" | "save" {
  return remoteName ? "save" : "viewer";
}

/** The prefix of each print's private temp directory. */
export const PRINT_DIR_PREFIX = "nautilus-print-";
/** A print directory older than this is swept on the next print: long
 * enough for the viewer to have the file open, short enough not to pile
 * up. */
export const PRINT_DIR_MAX_AGE_MS = 60 * 60 * 1000;

/** Write a print's PDF into a fresh private directory (mkdtemp: a unique
 * name, mode 0700), never a fixed shared path another user on the host
 * could pre-create or point elsewhere with a symlink. Returns the file. */
export async function writePrintFile(fileName: string, bytes: Uint8Array, tmp = os.tmpdir()): Promise<string> {
  const dir = await fs.promises.mkdtemp(path.join(tmp, PRINT_DIR_PREFIX));
  const file = path.join(dir, path.basename(fileName));
  // "wx": fail rather than follow anything already there.
  await fs.promises.writeFile(file, bytes, { flag: "wx", mode: 0o600 });
  return file;
}

/** Remove earlier prints' directories: ours (same owner, a real directory,
 * not a symlink) and older than `maxAgeMs`. Anything else, including
 * another user's, is left alone; errors are ignored (a viewer on Windows
 * may still hold the file). Returns the directories removed. */
export async function sweepPrintDirs(
  tmp = os.tmpdir(),
  maxAgeMs = PRINT_DIR_MAX_AGE_MS,
  now = Date.now()
): Promise<string[]> {
  const removed: string[] = [];
  let names: string[];
  try {
    names = await fs.promises.readdir(tmp);
  } catch {
    return removed;
  }
  const uid = typeof process.getuid === "function" ? process.getuid() : undefined;
  for (const name of names) {
    if (!name.startsWith(PRINT_DIR_PREFIX)) continue;
    const dir = path.join(tmp, name);
    try {
      const st = await fs.promises.lstat(dir);
      if (!st.isDirectory() || (uid !== undefined && st.uid !== uid)) continue;
      if (now - st.mtimeMs < maxAgeMs) continue;
      await fs.promises.rm(dir, { recursive: true, force: true });
      removed.push(dir);
    } catch {
      // not ours to remove, or already gone
    }
  }
  return removed;
}
