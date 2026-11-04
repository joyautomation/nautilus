// "nautilus: Export Diagram…" and "nautilus: Print Diagram…": an LD, FBD
// or SFC file to a print-friendly SVG or a paginated PDF.
//
// The drawing is not done in the webview. The diagram editors' layout code
// (webview-ui/src: ladderLayout, layout, sfc) is pure, so the exporter
// (webview-ui/src/export) runs it right here in the extension host, bundled
// to media/dist/diagram-export.cjs, from the same `naut … graph` model the
// editors render. That means an export needs no open diagram, cannot pick
// up a selection or a hover, and writes a PDF itself (a dozen PDF operators
// and the built-in Courier faces) instead of asking a webview to print —
// which VS Code webviews cannot reliably do on every platform.
//
// "Print" is an export to a temporary PDF (in a private mkdtemp directory)
// that opens in the system's PDF viewer, whose print dialog is the
// platform's own. In a remote window that viewer is out of reach, so Print
// saves the PDF through the save dialog instead.

import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";
import { fbdGraph } from "./fbdPreview";
import { ldGraph } from "./ldPreview";
import { sfcGraph } from "./sfcPreview";
import type { LiveValues } from "./liveValues";
import {
  diagramLangOf,
  diagramLangOfLanguage,
  diagramLangOfView,
  exportFileName,
  liveValueMap,
  orientationFor,
  paperFor,
  printRoute,
  sweepPrintDirs,
  writePrintFile,
  type DiagramLang,
  type ExportFormat,
  type Orientation,
  type Paper,
} from "./diagramExportLogic";

/** The bundled exporter's surface (webview-ui/src/export/index.ts). */
type Exporter = {
  exportSvg(lang: DiagramLang, model: unknown, opts?: { live?: unknown }): string;
  exportPdf(
    lang: DiagramLang,
    model: unknown,
    opts: { paper: Paper; orientation: Orientation; source?: string; live?: unknown }
  ): Uint8Array;
  liveSnapshot(values: Record<string, unknown>, format?: (v: unknown, label: string) => string): unknown;
};

let exporter: Exporter | undefined;
function loadExporter(context: vscode.ExtensionContext): Exporter {
  if (!exporter) {
    const file = vscode.Uri.joinPath(context.extensionUri, "media", "dist", "diagram-export.cjs").fsPath;
    if (!fs.existsSync(file)) {
      throw new Error(`the diagram exporter is missing (${file}) — build it with \`npm run build:webview\``);
    }
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    exporter = require(file) as Exporter;
  }
  return exporter;
}

type Target = { doc: vscode.TextDocument; lang: DiagramLang };

/** The diagram the command is about: the file it was invoked on (explorer
 * or editor-title menu), else whatever diagram is in front — a text
 * editor, an "Open With" diagram editor, or a diagram preview panel. */
async function resolveTarget(arg: unknown): Promise<Target | undefined> {
  if (arg instanceof vscode.Uri) {
    const lang = diagramLangOf(arg.path);
    if (lang) return { doc: await vscode.workspace.openTextDocument(arg), lang };
  }
  const ed = vscode.window.activeTextEditor?.document;
  const edLang = ed && (diagramLangOfLanguage(ed.languageId) ?? diagramLangOf(ed.uri.path));
  if (ed && edLang) return { doc: ed, lang: edLang };

  const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
  if (input instanceof vscode.TabInputCustom || input instanceof vscode.TabInputText) {
    const lang = diagramLangOf(input.uri.path);
    if (lang) return { doc: await vscode.workspace.openTextDocument(input.uri), lang };
  }
  // A preview panel follows the diagram file you were last in; take the
  // open document of that kind, preferring a visible one.
  if (input instanceof vscode.TabInputWebview) {
    const lang = diagramLangOfView(input.viewType);
    if (lang) {
      const of = (d: vscode.TextDocument) => (diagramLangOfLanguage(d.languageId) ?? diagramLangOf(d.uri.path)) === lang;
      const doc =
        vscode.window.visibleTextEditors.map((e) => e.document).find(of) ?? vscode.workspace.textDocuments.find(of);
      if (doc) return { doc, lang };
    }
  }
  return undefined;
}

async function graph(t: Target): Promise<{ model?: unknown; error?: string }> {
  const at = t.doc.uri.scheme === "file" ? t.doc.uri.fsPath : undefined;
  const src = t.doc.getText();
  if (t.lang === "ld") return ldGraph(src, at);
  if (t.lang === "sfc") return sfcGraph(src, at);
  const res = await fbdGraph(src, at);
  return "error" in res ? { error: res.error } : { model: res.model };
}

function pageOptions(source: string): { paper: Paper; orientation: Orientation; source: string } {
  const cfg = vscode.workspace.getConfiguration("nautilus.export");
  return {
    paper: paperFor(cfg.get<string>("paper"), Intl.DateTimeFormat().resolvedOptions().locale),
    orientation: orientationFor(cfg.get<string>("orientation")),
    source,
  };
}

/** Ask whether to include the live values — only when there are some. */
async function pickLive(live: LiveValues | undefined, ex: Exporter): Promise<unknown | null | undefined> {
  const snap = live?.snapshot();
  if (!live || !snap?.enabled || !snap.fresh) return undefined;
  const pick = await vscode.window.showQuickPick(
    [
      { label: "Program only", description: "the diagram as written", live: false },
      { label: "Include live values", description: "power flow, values and active steps as they are right now", live: true },
    ],
    { title: "nautilus: Export Diagram", placeHolder: "A controller is connected — include its live values?" }
  );
  if (!pick) return null; // cancelled
  if (!pick.live) return undefined;
  return ex.liveSnapshot(liveValueMap(snap.tags, snap.locals), (v, label) => live.display(v, label));
}

export function registerDiagramExport(context: vscode.ExtensionContext, live?: LiveValues): vscode.Disposable[] {
  return [
    vscode.commands.registerCommand("nautilus.diagram.export", (arg?: unknown, opts?: ExportArgs) =>
      exportDiagram(context, live, arg, opts)
    ),
    vscode.commands.registerCommand("nautilus.diagram.print", (arg?: unknown) => printDiagram(context, live, arg)),
  ];
}

/** Arguments that make the export non-interactive (tasks, keybindings,
 * the e2e suite): `executeCommand("nautilus.diagram.export", fileUri,
 * { format: "pdf", to: "/tmp/out.pdf" })`. Live values are never included
 * this way. Resolves to the written file. */
export type ExportArgs = { format?: ExportFormat; to?: string | vscode.Uri };

/** "nautilus: <message> <path>" with Open and Reveal buttons. In a remote
 * window the OS file manager is on the other machine, so Reveal shows the
 * file in VS Code's own Explorer instead. */
function showFileToast(message: string, file: vscode.Uri, warn = false): void {
  const open = "Open";
  const reveal = vscode.env.remoteName ? "Reveal in Explorer View" : "Reveal in File Explorer";
  const where = file.scheme === "file" ? file.fsPath : file.toString(true);
  const text = `nautilus: ${message} ${where}`;
  const shown = warn
    ? vscode.window.showWarningMessage(text, open, reveal)
    : vscode.window.showInformationMessage(text, open, reveal);
  void shown.then((choice) => {
    if (choice === open) void vscode.env.openExternal(file);
    else if (choice === reveal) {
      void vscode.commands.executeCommand(vscode.env.remoteName ? "revealInExplorer" : "revealFileInOS", file);
    }
  });
}

async function exportDiagram(
  context: vscode.ExtensionContext,
  live: LiveValues | undefined,
  arg?: unknown,
  opts?: ExportArgs,
  /** Skip the format pick (Print in a remote window saves a PDF). */
  preset?: ExportFormat
): Promise<vscode.Uri | undefined> {
  const target = await resolveTarget(arg);
  if (!target) {
    void vscode.window.showErrorMessage("nautilus: open a ladder (.ld, .L5X), FBD (.fbd) or SFC (.sfc) file to export its diagram");
    return undefined;
  }
  let ex: Exporter;
  try {
    ex = loadExporter(context);
  } catch (err) {
    void vscode.window.showErrorMessage("nautilus: " + (err as Error).message);
    return undefined;
  }
  if (opts?.format && opts.to) {
    const dest = typeof opts.to === "string" ? vscode.Uri.file(opts.to) : opts.to;
    const bytes = await render(ex, target, opts.format, undefined);
    if (!bytes) return undefined;
    await vscode.workspace.fs.writeFile(dest, bytes);
    return dest;
  }
  const formats = [
    { label: "PDF", description: "paginated for printing: page breaks between rungs and networks, name and page numbers on every page", format: "pdf" as ExportFormat },
    { label: "SVG", description: "the whole diagram as one vector image, for documents and slides", format: "svg" as ExportFormat },
  ];
  const fmt = preset
    ? formats.find((f) => f.format === preset)
    : await vscode.window.showQuickPick(formats, { title: "nautilus: Export Diagram", placeHolder: "Export as…" });
  if (!fmt) return undefined;
  const liveSnap = await pickLive(live, ex);
  if (liveSnap === null) return undefined;

  const name = path.basename(target.doc.uri.path);
  const dir =
    target.doc.uri.scheme === "file"
      ? vscode.Uri.file(path.dirname(target.doc.uri.fsPath))
      : vscode.workspace.workspaceFolders?.[0]?.uri;
  const fileName = exportFileName(name, fmt.format);
  const dest = await vscode.window.showSaveDialog({
    defaultUri: dir ? vscode.Uri.joinPath(dir, fileName) : undefined,
    filters: fmt.format === "pdf" ? { PDF: ["pdf"] } : { SVG: ["svg"] },
    saveLabel: preset ? "Save" : "Export",
    title: preset
      ? `Print ${name}: this is a remote window, so save the PDF (Show Local saves it on your machine) and print it from there`
      : `Export ${name} as ${fmt.label}`,
  });
  if (!dest) return undefined;

  const bytes = await render(ex, target, fmt.format, liveSnap);
  if (!bytes) return undefined;
  await vscode.workspace.fs.writeFile(dest, bytes);
  showFileToast(preset ? "saved the printable PDF to" : "exported to", dest);
  return dest;
}

async function printDiagram(context: vscode.ExtensionContext, live: LiveValues | undefined, arg?: unknown): Promise<void> {
  if (printRoute(vscode.env.remoteName) === "save") {
    await exportDiagram(context, live, arg, undefined, "pdf");
    return;
  }
  const target = await resolveTarget(arg);
  if (!target) {
    void vscode.window.showErrorMessage("nautilus: open a ladder (.ld, .L5X), FBD (.fbd) or SFC (.sfc) file to print its diagram");
    return;
  }
  let ex: Exporter;
  try {
    ex = loadExporter(context);
  } catch (err) {
    void vscode.window.showErrorMessage("nautilus: " + (err as Error).message);
    return;
  }
  const liveSnap = await pickLive(live, ex);
  if (liveSnap === null) return;
  const bytes = await render(ex, target, "pdf", liveSnap);
  if (!bytes) return;
  // Earlier prints' temp directories go once their viewer has had an hour.
  await sweepPrintDirs();
  const file = vscode.Uri.file(await writePrintFile(exportFileName(path.basename(target.doc.uri.path), "pdf"), bytes));
  // openExternal says true when it handed the file to the OS, not when a
  // viewer showed it (Linux with no PDF viewer says true too), so always
  // say where the file is.
  const opened = await vscode.env.openExternal(file);
  if (opened) showFileToast("opened the printable PDF in your PDF viewer:", file);
  else showFileToast("could not open a PDF viewer; the printable PDF is at", file, true);
}

async function render(ex: Exporter, t: Target, format: ExportFormat, liveSnap: unknown): Promise<Uint8Array | undefined> {
  const res = await graph(t);
  if (res.error || res.model === undefined) {
    void vscode.window.showErrorMessage("nautilus: cannot draw this diagram: " + (res.error ?? "no model"));
    return undefined;
  }
  try {
    if (format === "svg") return Buffer.from(ex.exportSvg(t.lang, res.model, { live: liveSnap }), "utf8");
    return ex.exportPdf(t.lang, res.model, { ...pageOptions(path.basename(t.doc.uri.path)), live: liveSnap });
  } catch (err) {
    void vscode.window.showErrorMessage("nautilus: export failed: " + String(err));
    return undefined;
  }
}
