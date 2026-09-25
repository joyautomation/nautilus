// FBD diagram preview + editor: webviews that render a .fbd file's diagram,
// live-updating as the text changes. The text is the source of truth; the
// diagram is a projection. The render model comes from `naut fbd graph -`
// and every edit gesture becomes a STRUCTURAL OP (`naut fbd edit`):
// the op is addressed by stable render-model ids, resolved in Go against a
// fresh parse of the current buffer, and comes back as minimal text edits —
// no consumer of the model ever computes source spans itself.
//
// Two hosts share this logic: the preview command's singleton panel (opens
// beside the text, follows the active .fbd editor, also renders diffs) and
// the CustomTextEditor ("Open With → FBD Diagram"), which ties a diagram
// per-document into VS Code's editor lifecycle.

import * as vscode from "vscode";
import { followActiveDoc } from "./previewFollow";
import { cliCommand, cliExecOptions, cliMissingMessage, isMissing } from "./cli";
import { execFile } from "child_process";
import * as path from "path";
import type { ProgramInfo } from "./onlineEdit";
import type { LiveValues } from "./liveValues";
import { gitShow } from "./gitHistory";
import { pickRevisions } from "./revisionPick";
import { applyDiagramKey, isDiagramKeyMessage, serialQueue, sourceDocument } from "./diagramKeys";
import { gateWebview } from "./webviewReady";

/** Mirror of lang/fbd.Model — see lang/fbd/graph.go for the contract. */
export type FbdModel = {
  name: string;
  nodes: FbdNode[];
  edges: FbdEdge[];
};
export type FbdNode = {
  id: string;
  kind: "input" | "block" | "fb" | "coil";
  label: string;
  type?: string;
  wire?: string;
  inputs?: string[];
  outputs?: string[];
  layer: number;
};
export type FbdEdge = {
  from: string;
  fromPin?: string;
  to: string;
  toPin?: string;
  wire?: string;
  negated?: boolean;
  feedback?: boolean;
};

/** Mirror of lang/fbd.EditOp — a structural edit addressed by model ids. */
export type FbdEditOp = {
  type:
    | "setLiteral"
    | "toggleNot"
    | "rewire"
    | "rename"
    | "deleteNode"
    | "insertStatement"
    | "setLayout"
    | "clearLayout"
    | "disconnect"
    | "addInput"
    | "declareVar"
    | "deleteVar"
    | "setComment"
    | "duplicate"
    | "retarget";
  node?: string;
  to?: string;
  toPin?: string;
  from?: string;
  fromPin?: string;
  value?: string;
  newName?: string;
  source?: string;
  sourcePin?: string;
  text?: string;
  x?: number;
  y?: number;
  entries?: { node: string; x: number; y: number }[];
	nodes?: string[];
  keepRefs?: boolean;
};

/** Mirror of lang/fbd.TextEdit: 1-based, end-exclusive. */
type FbdTextEdit = { line: number; col: number; endLine: number; endCol: number; newText: string };

type WebviewMessage =
  | { type: "edit"; op: FbdEditOp }
  | { type: "toggleLive" }
  | { type: "openPou"; pou: string };

const DEBOUNCE_MS = 150;

// ── CLI seam ───────────────────────────────────────────────────────────────

function cliPath(): string {
  return cliCommand();
}

/** Run `naut fbd graph -` over source text. */
export function fbdGraph(source: string): Promise<{ model: FbdModel } | { error: string }> {
  const cli = cliPath();
  return new Promise((resolve) => {
    const child = execFile(
      cli,
      ["fbd", "graph", "-"],
      cliExecOptions(),
      (err, stdout) => {
        // Exit 1 still writes {"error": ...} JSON on stdout — prefer it.
        try {
          const parsed = JSON.parse(stdout) as FbdModel & { error?: string };
          if (parsed.error) return resolve({ error: parsed.error });
          return resolve({ model: parsed });
        } catch {
          /* fall through */
        }
        if (isMissing(err)) return resolve({ error: cliMissingMessage(cli) });
        resolve({ error: err ? String(err) : "naut fbd graph: empty output" });
      }
    );
    child.stdin?.end(source);
  });
}

/** Run `naut fbd edit`: resolve op against source, get minimal edits. */
function fbdEdit(source: string, op: FbdEditOp): Promise<{ edits: FbdTextEdit[] } | { error: string }> {
  const cli = cliPath();
  return new Promise((resolve) => {
    const child = execFile(
      cli,
      ["fbd", "edit"],
      cliExecOptions(),
      (err, stdout) => {
        try {
          const parsed = JSON.parse(stdout) as { edits?: FbdTextEdit[]; error?: string };
          if (parsed.error) return resolve({ error: parsed.error });
          return resolve({ edits: parsed.edits ?? [] });
        } catch {
          /* fall through */
        }
        if (isMissing(err)) return resolve({ error: cliMissingMessage(cli) });
        resolve({ error: err ? String(err) : "naut fbd edit: empty output" });
      }
    );
    child.stdin?.end(JSON.stringify({ source, op }));
  });
}

// ── shared webview session logic ───────────────────────────────────────────

/** `forwardKeys`: the preview panels — a plain WebviewPanel has no document
 * for VS Code to undo or save, so the webview posts those keys to the host
 * (see diagramKeys.ts). The custom editors get them from VS Code natively. */
export function buildWebviewHtml(
  webview: vscode.Webview,
  extensionUri: vscode.Uri,
  opts: { forwardKeys?: boolean } = {}
): string {
  // The Svelte Flow editor bundle (webview-ui → media/dist): one JS + one
  // CSS, fully self-contained, CSP-pinned by nonce. The bundle speaks the
  // ready handshake, so arm it here — before any host post can race the
  // page's listener (webviewReady.ts).
  gateWebview(webview);
  const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "fbd-flow.js"));
  const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "fbd-flow.css"));
  const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="stylesheet" href="${styleUri}">
<title>FBD</title>
</head>
<body${opts.forwardKeys ? ' data-forward-keys="1"' : ""}>
<div id="app"></div>
<script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
}

export function webviewOptions(extensionUri: vscode.Uri, extraRoots: vscode.Uri[] = []): vscode.WebviewOptions {
  return {
    enableScripts: true,
    // extraRoots: the mimic/Component Editor add the user-components
    // storage directory here (see userComponents.ts) so their webview can
    // load the compiled bundle alongside the extension's own media/dist.
    localResourceRoots: [vscode.Uri.joinPath(extensionUri, "media"), ...extraRoots],
  };
}

/** Ops apply strictly in order: each must read the text AFTER the previous
 * one's edit landed, or rapid gestures (a multi-node drag, fast clicks)
 * rewrite the same region from stale text and drop each other's changes. */
let editQueue: Promise<void> = Promise.resolve();

function handleWebviewMessage(doc: vscode.TextDocument, msg: WebviewMessage): void {
  // The diagram toolbar's live pill drives the same command as the status
  // bar item — one toggle, every surface.
  if (msg.type === "toggleLive") {
    void vscode.commands.executeCommand("nautilus.liveValues.toggle");
    return;
  }
  // The instance inspector's "open source": jump to FUNCTION_BLOCK <pou>
  // in the project's library files.
  if (msg.type === "openPou") {
    void openPouSource(doc, msg.pou);
    return;
  }
  // The toolbar's divergence pill: open the visual diff against the live
  // controller program.
  if ((msg as { type?: string }).type === "diffLive") {
    void vscode.commands.executeCommand("nautilus.fbd.diffController");
    return;
  }
  editQueue = editQueue.then(() => applyOpMessage(doc, msg)).catch(() => undefined);
}

/** Find and reveal `FUNCTION_BLOCK <pou>` among the document's sibling .st
 * files (the project's libraries). Built-in blocks have no source to open. */
async function openPouSource(doc: vscode.TextDocument, pou: string): Promise<void> {
  const dir = vscode.Uri.joinPath(doc.uri, "..");
  const re = new RegExp(String.raw`^[ \t]*FUNCTION_BLOCK[ \t]+` + pou + String.raw`\b`, "im");
  try {
    const entries = await vscode.workspace.fs.readDirectory(dir);
    for (const [name, kind] of entries) {
      if (kind !== vscode.FileType.File || !/\.st$/i.test(name)) continue;
      const uri = vscode.Uri.joinPath(dir, name);
      const text = new TextDecoder().decode(await vscode.workspace.fs.readFile(uri));
      const m = re.exec(text);
      if (!m) continue;
      const opened = await vscode.workspace.openTextDocument(uri);
      const line = text.slice(0, m.index).split("\n").length - 1;
      const editor = await vscode.window.showTextDocument(opened, { preview: false });
      const pos = new vscode.Position(line, 0);
      editor.revealRange(new vscode.Range(pos, pos), vscode.TextEditorRevealType.InCenter);
      editor.selection = new vscode.Selection(pos, pos);
      return;
    }
  } catch {
    // fall through to the message below
  }
  void vscode.window.showInformationMessage(
    `nautilus: no FUNCTION_BLOCK ${pou} in this project's .st files — it's a built-in block`
  );
}

async function applyOpMessage(doc: vscode.TextDocument, msg: WebviewMessage): Promise<void> {
  if (msg.type !== "edit") return;
  // Belt and braces against xyflow selection-drag phantom entries: drop
  // anything without a node id before it reaches the CLI.
  if (msg.op.type === "setLayout" && msg.op.entries) {
    msg.op.entries = msg.op.entries.filter((e) => !!e.node);
    if (msg.op.entries.length === 0) return;
  }
  const res = await fbdEdit(doc.getText(), msg.op);
  if ("error" in res) {
    void vscode.window.showWarningMessage("nautilus: " + res.error);
    return;
  }
  if (res.edits.length === 0) return;
  const edit = new vscode.WorkspaceEdit();
  for (const e of res.edits) {
    edit.replace(
      doc.uri,
      new vscode.Range(e.line - 1, e.col - 1, e.endLine - 1, e.endCol - 1),
      e.newText
    );
  }
  await vscode.workspace.applyEdit(edit);
}

export function docTitle(doc: vscode.TextDocument): string {
  return path.basename(doc.uri.fsPath || doc.uri.path);
}

async function postModel(webview: vscode.Webview, doc: vscode.TextDocument): Promise<void> {
  const source = doc.getText();
  const res = await fbdGraph(source);
  if ("error" in res) {
    void webview.postMessage({ type: "error", message: res.error, title: docTitle(doc) });
  } else {
    // `source` rides along for the clipboard: a copy snapshots the text its
    // node ids resolve against, so a cut (or another file) can still paste.
    void webview.postMessage({ type: "model", model: res.model, title: docTitle(doc), source });
    postDiagnostics(webview, doc);
  }
}

/** Feed live controller values into the diagram for the webview's lifetime.
 * The same stream that drives text-editor pills fans out here, so the
 * diagram obeys the identical enable toggle and freshness window. */
export function attachLiveValues(live: LiveValues | undefined, panel: vscode.WebviewPanel): void {
  if (!live) return;
  const sub = live.addConsumer((frame) => {
    void panel.webview.postMessage({ type: "liveValues", ...frame });
  });
  panel.onDidDispose(() => sub.dispose());
}

/** Forward the document's squiggles into the diagram: the webview joins
 * them onto nodes by source line, so an error marks the offending block
 * with the same message the text editor shows. */
// ── controller sync state, shown IN the diagrams ───────────────────────────
// Divergence from the live program shouldn't only live in the status bar
// (which the graphical editors never focus): every diagram webview registers
// here, and OnlineEdit's poll pushes its verdict for the program it checked.
// Panels showing a different document get "unknown" (no pill) rather than a
// verdict that wasn't computed for them.
type SyncTarget = { webview: vscode.Webview; doc: () => vscode.Uri | undefined };
const syncTargets = new Set<SyncTarget>();
let lastSync: { state: string; programUri?: vscode.Uri } | undefined;

export function addSyncTarget(
  webview: vscode.Webview,
  doc: () => vscode.Uri | undefined
): vscode.Disposable {
  const t: SyncTarget = { webview, doc };
  syncTargets.add(t);
  if (lastSync) postSync(t, lastSync.state, lastSync.programUri);
  return new vscode.Disposable(() => syncTargets.delete(t));
}

export function broadcastSyncState(state: string, programUri?: vscode.Uri): void {
  lastSync = { state, programUri };
  for (const t of syncTargets) postSync(t, state, programUri);
}

function postSync(t: SyncTarget, state: string, programUri?: vscode.Uri): void {
  const mine = programUri && t.doc()?.toString() === programUri.toString();
  void t.webview.postMessage({ type: "syncState", state: mine ? state : "unknown" });
}

export function postDiagnostics(webview: vscode.Webview, doc: vscode.TextDocument): void {
  const diags = vscode.languages.getDiagnostics(doc.uri).map((d) => ({
    line: d.range.start.line + 1,
    message: d.message,
    severity: d.severity === vscode.DiagnosticSeverity.Warning ? "warning" : "error",
  }));
  void webview.postMessage({ type: "diagnostics", diags });
}

// ── the preview command's singleton panel ──────────────────────────────────

export class FbdPreview implements vscode.Disposable {
  private panel?: vscode.WebviewPanel;
  private docUri?: vscode.Uri;
  private debounce?: NodeJS.Timeout;
  private disposables: vscode.Disposable[] = [];
  /** Set while diffing: the frozen base + title. Edits RE-DIFF against
   * it so the overlay tracks changes live; "exit diff" or reopening the
   * preview leaves diff mode. */
  private diffBase?: { src: string; label: string; title: string; headSrc?: string };
  private get diffing(): boolean {
    return this.diffBase !== undefined;
  }

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly live?: LiveValues
  ) {
    this.disposables.push(
      vscode.workspace.onDidChangeTextDocument((e) => {
        if (this.panel && e.document.uri.toString() === this.docUri?.toString()) {
          this.scheduleUpdate(e.document);
        }
      }),
      vscode.window.onDidChangeActiveTextEditor((ed) => {
        // Follow the active .fbd file, like the markdown preview.
        if (this.panel && ed && ed.document.languageId === "iec-fbd") {
          // Diff state belongs to one document: kept when you click into
          // the same file's text, dropped when the preview moves to another.
          const next = followActiveDoc(
            { docUri: this.docUri?.toString(), diffBase: this.diffBase },
            ed.document.uri.toString()
          );
          this.docUri = ed.document.uri;
          this.diffBase = next.diffBase;
          this.scheduleUpdate(ed.document);
        }
      }),
      vscode.languages.onDidChangeDiagnostics((e) => {
        if (!this.panel || this.diffing || !this.docUri) return;
        if (!e.uris.some((u) => u.toString() === this.docUri?.toString())) return;
        void sourceDocument(this.docUri).then((doc) => {
          if (doc && this.panel) postDiagnostics(this.panel.webview, doc);
        });
      })
    );
  }

  /** Open (or reveal) the preview panel for the active .fbd editor. */
  async preview(): Promise<void> {
    const doc = await this.activeFbdDoc();
    if (!doc) return;
    this.docUri = doc.uri;
    this.diffBase = undefined;
    this.ensurePanel();
    await this.update(doc);
  }

  /** Visual diff: the working tree (current buffer) vs git HEAD. */
  async diff(): Promise<void> {
    const doc = await this.activeFbdDoc();
    if (!doc) return;
    if (doc.uri.scheme !== "file") {
      void vscode.window.showErrorMessage("nautilus: FBD diff needs a file on disk");
      return;
    }
    const baseSrc = await gitShowHead(doc.uri.fsPath);
    if (baseSrc === undefined) {
      void vscode.window.showErrorMessage(
        `nautilus: ${path.basename(doc.uri.fsPath)} has no committed version (not in git HEAD)`
      );
      return;
    }
    await this.showDiff(doc, baseSrc, "in git HEAD", `${docTitle(doc)} — HEAD ↔ working tree`);
  }

  /** Visual diff: the working tree (current buffer) vs what the controller
   * is running. The controller serves its ORIGINAL program source, so an
   * .fbd program diffs as two render models — the wiring review, live. */
  async diffController(): Promise<void> {
    const doc = await this.activeFbdDoc();
    if (!doc) return;
    const info = await fetchControllerProgram(doc.getText());
    if (!info) return;
    if (info.language !== "fbd") {
      void vscode.window.showErrorMessage(
        `nautilus: the controller runs this program as ${info.language ?? "st"} — use "Diff Program with Controller" for the text diff`
      );
      return;
    }
    await this.showDiff(
      doc,
      info.source,
      "in the controller's program",
      `${docTitle(doc)} — controller ${info.hash}${info.dirty ? " · online edit" : ""} ↔ workspace`
    );
  }

  /** Visual diff between any two revisions of the file in git — or one
   * revision and the working tree. With two commits chosen both sides are
   * frozen, so edits leave the overlay alone. */
  async diffRevisions(): Promise<void> {
    const doc = await this.activeFbdDoc();
    if (!doc) return;
    if (doc.uri.scheme !== "file") {
      void vscode.window.showErrorMessage("nautilus: FBD diff needs a file on disk");
      return;
    }
    const pair = await pickRevisions(doc.uri.fsPath);
    if (!pair) return;
    await this.showDiff(
      doc,
      pair.base.src,
      `at ${pair.base.label}`,
      `${docTitle(doc)} — ${pair.base.label} ↔ ${pair.head?.label ?? "working tree"}`,
      pair.head?.src
    );
  }

  /** Enter diff mode: freeze the base (and the head, when given) and post
   * the first overlay. */
  private async showDiff(
    doc: vscode.TextDocument,
    baseSrc: string,
    baseLabel: string,
    title: string,
    headSrc?: string
  ): Promise<void> {
    this.docUri = doc.uri;
    this.ensurePanel();
    this.diffBase = { src: baseSrc, label: baseLabel, title, headSrc };
    await this.postDiff(doc);
  }

  /** Graph the frozen base + the CURRENT text and post the overlay. */
  private async postDiff(doc: vscode.TextDocument): Promise<void> {
    if (!this.panel || !this.diffBase) return;
    const { src, label, title, headSrc } = this.diffBase;
    const [base, head] = await Promise.all([fbdGraph(src), fbdGraph(headSrc ?? doc.getText())]);
    if ("error" in base || "error" in head) {
      // Mid-edit the head may not parse for a moment — stay in diff mode,
      // surface the message, and the next edit re-diffs.
      const msg = ("error" in head ? head.error : "") || ("error" in base ? `${label}: ${base.error}` : "");
      this.post({ type: "error", message: msg, title: docTitle(doc) });
      return;
    }
    this.post({ type: "diff", base: base.model, head: head.model, title });
  }

  private async activeFbdDoc(): Promise<vscode.TextDocument | undefined> {
    const doc = vscode.window.activeTextEditor?.document;
    if (doc && doc.languageId === "iec-fbd") return doc;
    // The diagram custom editor never appears in activeTextEditor — resolve
    // its document through the active tab instead.
    const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
    if (input instanceof vscode.TabInputCustom && input.uri.path.toLowerCase().endsWith(".fbd")) {
      const custom = vscode.workspace.textDocuments.find(
        (d) => d.uri.toString() === (input as vscode.TabInputCustom).uri.toString()
      );
      if (custom) return custom;
    }
    // The preview panel may have focus; fall back to the tracked document.
    const tracked = await sourceDocument(this.docUri);
    if (tracked) return tracked;
    void vscode.window.showErrorMessage("nautilus: open a .fbd file first");
    return undefined;
  }

  private scheduleUpdate(doc: vscode.TextDocument): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.debounce = setTimeout(() => void this.update(doc), DEBOUNCE_MS);
  }

  private async update(doc: vscode.TextDocument): Promise<void> {
    if (!this.panel) return;
    if (this.diffBase) {
      // Two frozen revisions don't move with the buffer.
      if (this.diffBase.headSrc === undefined) await this.postDiff(doc);
      return;
    }
    const source = doc.getText();
    const res = await fbdGraph(source);
    if ("error" in res) {
      this.post({ type: "error", message: res.error, title: docTitle(doc) });
    } else {
      this.post({ type: "model", model: res.model, title: docTitle(doc), source });
      postDiagnostics(this.panel.webview, doc);
    }
  }

  private ensurePanel(): void {
    if (this.panel) {
      this.panel.reveal(undefined, true);
      return;
    }
    this.panel = vscode.window.createWebviewPanel(
      "nautilusFbdPreview",
      "FBD Preview",
      { viewColumn: vscode.ViewColumn.Beside, preserveFocus: true },
      { ...webviewOptions(this.context.extensionUri), retainContextWhenHidden: true }
    );
    this.panel.onDidDispose(() => {
      this.panel = undefined;
      this.diffBase = undefined;
    });
    // The source resolves through sourceDocument (open in an editor or
    // not — a closed text tab must not strand the preview's gestures), one
    // message at a time so an edit never overtakes the one before it.
    const inOrder = serialQueue();
    this.panel.webview.onDidReceiveMessage((msg: WebviewMessage) => {
      if (msg.type === "toggleLive") {
        void vscode.commands.executeCommand("nautilus.liveValues.toggle");
        return;
      }
      void inOrder(async () => {
        const key: unknown = msg;
        if (isDiagramKeyMessage(key)) {
          const doc = await sourceDocument(this.docUri);
          if (doc && this.panel) void applyDiagramKey(key.action, doc, this.panel, { diffing: this.diffing });
          return;
        }
        if ((msg as { type?: string }).type === "exitDiff") {
          this.diffBase = undefined;
          const doc = await sourceDocument(this.docUri);
          if (doc) void this.update(doc);
          return;
        }
        if (this.diffing || !this.docUri) return;
        const doc = await sourceDocument(this.docUri);
        if (doc) handleWebviewMessage(doc, msg);
      });
    });
    this.panel.webview.html = buildWebviewHtml(this.panel.webview, this.context.extensionUri, {
      forwardKeys: true,
    });
    attachLiveValues(this.live, this.panel);
    const sync = addSyncTarget(this.panel.webview, () => this.docUri);
    this.panel.onDidDispose(() => sync.dispose());
  }

  private post(msg: unknown): void {
    if (!this.panel) return;
    this.panel.title = "FBD: " + ((msg as { title?: string }).title ?? "Preview");
    void this.panel.webview.postMessage(msg);
  }

  dispose(): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.panel?.dispose();
    for (const d of this.disposables) d.dispose();
  }
}

// ── CustomTextEditor: "Open With → FBD Diagram" ────────────────────────────

/** The diagram as a real editor over the .fbd document: registered with
 * priority "option" so plain text stays the default, tied to the document's
 * lifecycle (undo, dirty state, revert all belong to the TextDocument — our
 * edits are ordinary WorkspaceEdits against it). */
export class FbdEditorProvider implements vscode.CustomTextEditorProvider {
  static readonly viewType = "nautilus.fbdDiagram";

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly live?: LiveValues
  ) {}

  register(): vscode.Disposable {
    return vscode.window.registerCustomEditorProvider(FbdEditorProvider.viewType, this, {
      webviewOptions: { retainContextWhenHidden: true },
      supportsMultipleEditorsPerDocument: true,
    });
  }

  async resolveCustomTextEditor(
    document: vscode.TextDocument,
    panel: vscode.WebviewPanel
  ): Promise<void> {
    panel.webview.options = webviewOptions(this.context.extensionUri);
    panel.webview.html = buildWebviewHtml(panel.webview, this.context.extensionUri);

    let debounce: NodeJS.Timeout | undefined;
    const changeSub = vscode.workspace.onDidChangeTextDocument((e) => {
      if (e.document.uri.toString() !== document.uri.toString()) return;
      if (debounce) clearTimeout(debounce);
      debounce = setTimeout(() => void postModel(panel.webview, document), DEBOUNCE_MS);
    });
    const messageSub = panel.webview.onDidReceiveMessage((msg: WebviewMessage) => {
      void handleWebviewMessage(document, msg);
    });
    const diagSub = vscode.languages.onDidChangeDiagnostics((e) => {
      if (e.uris.some((u) => u.toString() === document.uri.toString())) {
        postDiagnostics(panel.webview, document);
      }
    });
    const syncSub = addSyncTarget(panel.webview, () => document.uri);
    panel.onDidDispose(() => {
      if (debounce) clearTimeout(debounce);
      changeSub.dispose();
      messageSub.dispose();
      diagSub.dispose();
      syncSub.dispose();
    });
    attachLiveValues(this.live, panel);

    await postModel(panel.webview, document);
  }
}

/** Fetch the controller's program for the POU the given source declares —
 * multi-task controllers serve each task's ORIGINAL source by POU name.
 * Shows the connection error itself; returns undefined on failure. */
export async function fetchControllerProgram(localSrc: string): Promise<ProgramInfo | undefined> {
  const url = vscode.workspace
    .getConfiguration("nautilus")
    .get<string>("runtimeUrl", "http://localhost:8080")
    .replace(/\/+$/, "");
  const pou = /^\s*PROGRAM\s+([A-Za-z_][A-Za-z0-9_]*)/m.exec(localSrc)?.[1];
  try {
    if (pou) {
      const res = await fetch(url + "/api/program?pou=" + encodeURIComponent(pou));
      if (res.ok) return (await res.json()) as ProgramInfo;
      if (res.status !== 404) throw new Error(res.statusText);
    }
    const res = await fetch(url + "/api/program");
    if (!res.ok) throw new Error(res.statusText);
    return (await res.json()) as ProgramInfo;
  } catch {
    void vscode.window.showErrorMessage(`nautilus: no controller at ${url}`);
    return undefined;
  }
}

/** The file's content at git HEAD, or undefined if untracked/not a repo.
 * (`gitShow` in gitHistory.ts is the general form; this stays as the
 * three previews' HEAD-diff entry point.) */
export function gitShowHead(fsPath: string): Promise<string | undefined> {
  return gitShow(fsPath, "HEAD");
}
