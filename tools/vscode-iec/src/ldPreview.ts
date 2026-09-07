// Ladder Diagram preview: renders a .ld file's rungs in the shared diagram
// webview (App switches to ladder mode on the ldModel message). Layout is
// canonical — the drawing is a pure function of the source — and live
// values paint power flow through contacts, branches, and coils. Read-only
// for now: edit the text, the ladder follows.

import { execFile } from "child_process";
import * as vscode from "vscode";
import { LiveValues } from "./liveValues";
import {
  addSyncTarget,
  attachLiveValues,
  buildWebviewHtml,
  docTitle,
  fetchControllerProgram,
  gitShowHead,
  postDiagnostics,
  webviewOptions,
} from "./fbdPreview";

/** Run `nautilus ld graph -` over source text. `at` is the path the buffer
 * belongs to, so the project's library files — a PROGRAM-less .ld/.fbd/.st
 * holding FUNCTION_BLOCKs — are in scope for a user block's power pins. */
function ldGraph(source: string, at?: string): Promise<{ model?: unknown; error?: string }> {
  const cli = vscode.workspace.getConfiguration("nautilus").get<string>("cliPath", "nautilus");
  const args = at ? ["ld", "graph", "-", at] : ["ld", "graph", "-"];
  return new Promise((resolve) => {
    const child = execFile(cli, args, { maxBuffer: 16 * 1024 * 1024 }, (err, stdout) => {
      try {
        const parsed = JSON.parse(stdout) as { error?: string };
        if (parsed.error) return resolve({ error: parsed.error });
        return resolve({ model: parsed });
      } catch {
        // fall through
      }
      resolve({ error: err ? String(err) : "nautilus ld graph: empty output" });
    });
    child.stdin?.end(source);
  });
}

type LdTextEdit = { line: number; col: number; endLine: number; endCol: number; newText: string };

// Every gesture and op lands here ("Output → nautilus ladder") — when an
// edit seems to do nothing, this channel says whether the webview fired,
// what op was posted, and how Go answered.
let ldLog: vscode.OutputChannel | undefined;
function logLd(msg: string): void {
  ldLog ??= vscode.window.createOutputChannel("nautilus ladder");
  ldLog.appendLine(`[${new Date().toISOString()}] ${msg}`);
}

/** Run `nautilus ld edit`: resolve op against source, get rung-level edits. */
function ldEdit(source: string, op: unknown, at?: string): Promise<{ edits?: LdTextEdit[]; error?: string }> {
  const cli = vscode.workspace.getConfiguration("nautilus").get<string>("cliPath", "nautilus");
  return new Promise((resolve) => {
    const child = execFile(cli, ["ld", "edit"], { maxBuffer: 16 * 1024 * 1024 }, (err, stdout) => {
      try {
        const parsed = JSON.parse(stdout) as { edits?: LdTextEdit[]; error?: string };
        if (parsed.error) return resolve({ error: parsed.error });
        return resolve({ edits: parsed.edits ?? [] });
      } catch {
        // fall through
      }
      resolve({ error: err ? String(err) : "nautilus ld edit: empty output" });
    });
    child.stdin?.end(JSON.stringify({ source, op, file: at }));
  });
}

/** Ladder ops apply strictly in order (same rule as the FBD queue): each
 * must read the text AFTER the previous edit landed. */
let ldEditQueue: Promise<void> = Promise.resolve();

function handleLdMessage(doc: vscode.TextDocument, msg: { type?: string; op?: unknown }): void {
  if (msg?.type === "toggleLive") {
    void vscode.commands.executeCommand("nautilus.liveValues.toggle");
    return;
  }
  if (msg?.type === "ldTrace") {
    logLd("webview: " + String((msg as { msg?: unknown }).msg ?? ""));
    return;
  }
  if (msg?.type === "diffLive") {
    void vscode.commands.executeCommand("nautilus.ld.diffController");
    return;
  }
  if (msg?.type !== "ldEdit" || !msg.op) return;
  ldEditQueue = ldEditQueue
    .then(async () => {
      logLd("op: " + JSON.stringify(msg.op));
      const res = await ldEdit(doc.getText(), msg.op, docPath(doc));
      if (res.error) {
        logLd("  -> refused: " + res.error);
        void vscode.window.showWarningMessage("nautilus: " + res.error);
        return;
      }
      if (!res.edits || res.edits.length === 0) {
        logLd("  -> no edits");
        return;
      }
      const edit = new vscode.WorkspaceEdit();
      for (const e of res.edits) {
        edit.replace(
          doc.uri,
          new vscode.Range(e.line - 1, e.col - 1, e.endLine - 1, e.endCol - 1),
          e.newText
        );
      }
      const ok = await vscode.workspace.applyEdit(edit);
      logLd(`  -> ${res.edits.length} edit(s), applied=${ok}`);
    })
    .catch((err) => logLd("  -> exception: " + String(err)));
}

const DEBOUNCE_MS = 250;

/** The on-disk path a document belongs to, for library resolution;
 * undefined for untitled/virtual documents, which have no project. */
function docPath(doc: vscode.TextDocument): string | undefined {
  return doc.uri.scheme === "file" ? doc.uri.fsPath : undefined;
}

/** Post the ladder model (or the parse error) into a webview. */
async function postLdModel(webview: vscode.Webview, doc: vscode.TextDocument): Promise<void> {
  const res = await ldGraph(doc.getText(), docPath(doc));
  if (res.error) {
    void webview.postMessage({ type: "error", message: res.error, title: docTitle(doc) });
  } else {
    void webview.postMessage({ type: "ldModel", model: res.model, title: docTitle(doc) });
    postDiagnostics(webview, doc);
  }
}

/** "Open With → Ladder Diagram": the ladder as a real editor over the .ld
 * document, exactly like the FBD custom editor (text remains the default).
 * The view is a faithful projection: edit the text — including through the
 * text editor side by side — and the ladder follows. */
export class LdEditorProvider implements vscode.CustomTextEditorProvider {
  static readonly viewType = "nautilus.ldDiagram";

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly live?: LiveValues
  ) {}

  register(): vscode.Disposable {
    return vscode.window.registerCustomEditorProvider(LdEditorProvider.viewType, this, {
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
      debounce = setTimeout(() => void postLdModel(panel.webview, document), DEBOUNCE_MS);
    });
    const messageSub = panel.webview.onDidReceiveMessage((msg: { type?: string; op?: unknown }) => {
      handleLdMessage(document, msg);
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

    await postLdModel(panel.webview, document);
  }
}

export class LdPreview implements vscode.Disposable {
  private panel?: vscode.WebviewPanel;
  private docUri?: vscode.Uri;
  private debounce?: NodeJS.Timeout;
  /** Set while diffing: the frozen base source + title. Edits RE-DIFF
   * against it (the overlay tracks your changes live); the toolbar's
   * "exit diff" or reopening the preview leaves diff mode. */
  private diffBase?: { src: string; title: string };
  private disposables: vscode.Disposable[] = [];

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
        // Follow the active .ld file, like the markdown preview.
        if (this.panel && ed && ed.document.languageId === "iec-ld") {
          this.docUri = ed.document.uri;
          this.scheduleUpdate(ed.document);
        }
      }),
      vscode.languages.onDidChangeDiagnostics((e) => {
        if (!this.panel || !this.docUri) return;
        if (!e.uris.some((u) => u.toString() === this.docUri?.toString())) return;
        const doc = vscode.workspace.textDocuments.find((d) => d.uri.toString() === this.docUri?.toString());
        if (doc) postDiagnostics(this.panel.webview, doc);
      })
    );
  }

  /** The .ld document the user is "in": active editor, active diagram tab,
   * the tracked one, or any open .ld as a last resort. */
  private activeLdDoc(): vscode.TextDocument | undefined {
    const ed = vscode.window.activeTextEditor?.document;
    if (ed && ed.languageId === "iec-ld") return ed;
    const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
    if (input instanceof vscode.TabInputCustom && input.uri.path.toLowerCase().endsWith(".ld")) {
      const custom = vscode.workspace.textDocuments.find(
        (d) => d.uri.toString() === (input as vscode.TabInputCustom).uri.toString()
      );
      if (custom) return custom;
    }
    const tracked = vscode.workspace.textDocuments.find(
      (d) => d.uri.toString() === this.docUri?.toString()
    );
    if (tracked) return tracked;
    return vscode.workspace.textDocuments.find((d) => d.languageId === "iec-ld");
  }

  /** Visual diff: the working tree (current buffer) vs git HEAD. */
  async diff(): Promise<void> {
    const doc = this.activeLdDoc();
    if (!doc) {
      void vscode.window.showErrorMessage("nautilus: open a .ld file first");
      return;
    }
    if (doc.uri.scheme !== "file") {
      void vscode.window.showErrorMessage("nautilus: ladder diff needs a file on disk");
      return;
    }
    const baseSrc = await gitShowHead(doc.uri.fsPath);
    if (baseSrc === undefined) {
      void vscode.window.showErrorMessage(
        `nautilus: ${doc.uri.path.split("/").pop()} has no committed version (not in git HEAD)`
      );
      return;
    }
    await this.showDiff(doc, baseSrc, `${docTitle(doc)} — HEAD ↔ working tree`);
  }

  /** Visual diff: the working tree vs what the controller is running — the
   * controller serves each task's ORIGINAL source, so .ld diffs as rungs. */
  async diffController(): Promise<void> {
    const doc = this.activeLdDoc();
    if (!doc) {
      void vscode.window.showErrorMessage("nautilus: open a .ld file first");
      return;
    }
    const info = await fetchControllerProgram(doc.getText());
    if (!info) return;
    if (info.language !== "ld") {
      void vscode.window.showErrorMessage(
        `nautilus: the controller runs this program as ${info.language ?? "st"} — use "Diff Program with Controller" for the text diff`
      );
      return;
    }
    await this.showDiff(
      doc,
      info.source,
      `${docTitle(doc)} — controller ${info.hash}${info.dirty ? " · online edit" : ""} ↔ workspace`
    );
  }

  /** Enter diff mode: freeze the base and post the first overlay. */
  private async showDiff(doc: vscode.TextDocument, baseSrc: string, title: string): Promise<void> {
    this.docUri = doc.uri;
    this.ensurePanel();
    this.diffBase = { src: baseSrc, title };
    await this.postDiff(doc);
  }

  /** Graph the frozen base + the CURRENT text and post the overlay. */
  private async postDiff(doc: vscode.TextDocument): Promise<void> {
    if (!this.panel || !this.diffBase) return;
    const at = docPath(doc);
    const [base, head] = await Promise.all([ldGraph(this.diffBase.src, at), ldGraph(doc.getText(), at)]);
    if (base.error || head.error) {
      // Mid-edit the head may not parse for a moment — stay in diff mode,
      // surface the message, and the next edit re-diffs.
      void this.panel.webview.postMessage({
        type: "error",
        message: head.error ?? base.error,
        title: docTitle(doc),
      });
      return;
    }
    this.panel.title = "Ladder: diff";
    void this.panel.webview.postMessage({
      type: "ldDiff",
      base: base.model,
      head: head.model,
      title: this.diffBase.title,
    });
  }

  /** Open (or reveal) the ladder preview for the active .ld editor. */
  async preview(): Promise<void> {
    const doc = this.activeLdDoc();
    if (!doc) {
      void vscode.window.showErrorMessage("nautilus: open a .ld file to preview its ladder");
      return;
    }
    this.docUri = doc.uri;
    this.diffBase = undefined;
    this.ensurePanel();
    await this.update(doc);
  }

  private ensurePanel(): void {
    if (!this.panel) {
      this.panel = vscode.window.createWebviewPanel(
        "nautilus.ldPreview",
        "Ladder",
        vscode.ViewColumn.Beside,
        webviewOptions(this.context.extensionUri)
      );
      this.panel.webview.html = buildWebviewHtml(this.panel.webview, this.context.extensionUri);
      this.panel.webview.onDidReceiveMessage((msg: { type?: string; op?: unknown }) => {
        const doc = vscode.workspace.textDocuments.find(
          (d) => d.uri.toString() === this.docUri?.toString()
        );
        if (msg?.type === "exitDiff") {
          this.diffBase = undefined;
          if (doc) void this.update(doc);
          return;
        }
        if (doc) handleLdMessage(doc, msg);
        else if (msg?.type === "toggleLive") void vscode.commands.executeCommand("nautilus.liveValues.toggle");
      });
      attachLiveValues(this.live, this.panel);
      const sync = addSyncTarget(this.panel.webview, () => this.docUri);
      this.panel.onDidDispose(() => {
        sync.dispose();
        this.panel = undefined;
        this.diffBase = undefined;
      });
    } else {
      this.panel.reveal(undefined, true);
    }
  }

  private scheduleUpdate(doc: vscode.TextDocument): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.debounce = setTimeout(() => void this.update(doc), 250);
  }

  private async update(doc: vscode.TextDocument): Promise<void> {
    if (!this.panel) return;
    // A text change while diffing keeps the diff LIVE: re-overlay the
    // current text onto the frozen base.
    if (this.diffBase) {
      await this.postDiff(doc);
      return;
    }
    this.panel.title = "Ladder: " + docTitle(doc);
    await postLdModel(this.panel.webview, doc);
  }

  dispose(): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.panel?.dispose();
    for (const d of this.disposables) d.dispose();
  }
}
