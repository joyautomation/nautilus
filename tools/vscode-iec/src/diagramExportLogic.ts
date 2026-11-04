// The decisions behind "Export Diagram…" and "Print Diagram…" that do not
// need VS Code: which diagram a file is, what the exported file is called,
// which paper it prints on. The drawing itself lives in
// webview-ui/src/export (bundled to media/dist/diagram-export.cjs).

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

/** The diagram behind one of the extension's webview panels — the
 * "Open … Preview" panels and the "Open With" diagram editors. */
export function diagramLangOfView(viewType: string | undefined): DiagramLang | undefined {
  // A webview tab's viewType arrives prefixed ("mainThreadWebview-…") on
  // some VS Code versions, so match the end.
  const m = /nautilus\.(ld|l5x|fbd|sfc)(Preview|Diagram)$/.exec(viewType ?? "");
  if (!m) return undefined;
  return m[1] === "l5x" ? "ld" : (m[1] as DiagramLang);
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
