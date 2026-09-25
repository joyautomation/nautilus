// SFC (Sequential Function Chart) preview: renders a .sfc file's chart in
// the shared diagram webview (App switches to SFC mode on the sfcModel
// message — the same bundle FBD/Ladder share, see fbdPreview.ts's header).
// Structure (steps/transitions/branches) is canonical in the text, exactly
// like Ladder; the webview derives layout from topology (lang/sfc/graph.go
// §4.1) and every gesture round-trips through `naut sfc edit` as a
// structural op, resolved against a fresh parse, returning minimal
// TextEdits — the identical contract `fbd.ApplyEdit`/`ld.ApplyEdit` use.
//
// Visual diff (vs git HEAD / vs the controller): the SAME two source
// options and command naming FBD/Ladder use (nautilus.sfc.diff /
// .diffController), graphing base and head as two separate models and
// letting the webview's diffSfc (sfc.ts) overlay them — the merge itself
// never touches this file.

import { execFile } from "child_process";
import * as vscode from "vscode";
import { applyDiagramKey, isDiagramKeyMessage } from "./diagramKeys";
import { followActiveDoc } from "./previewFollow";
import { cliCommand, cliExecOptions, cliMissingMessage, isMissing } from "./cli";
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
import { pickRevisions } from "./revisionPick";

/** Run `naut sfc graph -` over source text. */
function sfcGraph(source: string): Promise<{ model?: unknown; error?: string }> {
  const cli = cliCommand();
  return new Promise((resolve) => {
    const child = execFile(cli, ["sfc", "graph", "-"], cliExecOptions(), (err, stdout) => {
      try {
        const parsed = JSON.parse(stdout) as { error?: string };
        if (parsed.error) return resolve({ error: parsed.error });
        return resolve({ model: parsed });
      } catch {
        // fall through
      }
      if (isMissing(err)) return resolve({ error: cliMissingMessage(cli) });
      resolve({ error: err ? String(err) : "naut sfc graph: empty output" });
    });
    child.stdin?.end(source);
  });
}

type SfcTextEdit = { line: number; col: number; endLine: number; endCol: number; newText: string };

/** Run `naut sfc edit`: resolve op against source, get minimal edits. */
function sfcEdit(source: string, op: unknown): Promise<{ edits?: SfcTextEdit[]; error?: string }> {
  const cli = cliCommand();
  return new Promise((resolve) => {
    const child = execFile(cli, ["sfc", "edit"], cliExecOptions(), (err, stdout) => {
      try {
        const parsed = JSON.parse(stdout) as { edits?: SfcTextEdit[]; error?: string };
        if (parsed.error) return resolve({ error: parsed.error });
        return resolve({ edits: parsed.edits ?? [] });
      } catch {
        // fall through
      }
      if (isMissing(err)) return resolve({ error: cliMissingMessage(cli) });
      resolve({ error: err ? String(err) : "naut sfc edit: empty output" });
    });
    child.stdin?.end(JSON.stringify({ source, op }));
  });
}

/** SFC ops apply strictly in order (same rule as the FBD/Ladder queues):
 * each must read the text AFTER the previous edit landed. */
let sfcEditQueue: Promise<void> = Promise.resolve();

function handleSfcMessage(doc: vscode.TextDocument, msg: { type?: string; op?: unknown }): void {
  if (msg?.type === "toggleLive") {
    void vscode.commands.executeCommand("nautilus.liveValues.toggle");
    return;
  }
  // The toolbar's divergence pill: open the visual diff against the live
  // controller program, exactly like FBD/Ladder's own pill.
  if (msg?.type === "diffLive") {
    void vscode.commands.executeCommand("nautilus.sfc.diffController");
    return;
  }
  if (msg?.type !== "sfcEdit" || !msg.op) return;
  sfcEditQueue = sfcEditQueue
    .then(async () => {
      const res = await sfcEdit(doc.getText(), msg.op);
      if (res.error) {
        void vscode.window.showWarningMessage("nautilus: " + res.error);
        return;
      }
      if (!res.edits || res.edits.length === 0) return;
      const edit = new vscode.WorkspaceEdit();
      for (const e of res.edits) {
        edit.replace(
          doc.uri,
          new vscode.Range(e.line - 1, e.col - 1, e.endLine - 1, e.endCol - 1),
          e.newText
        );
      }
      await vscode.workspace.applyEdit(edit);
    })
    .catch(() => undefined);
}

const DEBOUNCE_MS = 250;

/** Post the SFC model (or the parse error) into a webview. */
async function postSfcModel(webview: vscode.Webview, doc: vscode.TextDocument): Promise<void> {
  const res = await sfcGraph(doc.getText());
  if (res.error) {
    void webview.postMessage({ type: "error", message: res.error, title: docTitle(doc), lang: "sfc" });
  } else {
    void webview.postMessage({ type: "sfcModel", model: res.model, title: docTitle(doc) });
    postDiagnostics(webview, doc);
  }
}

/** "Open With → SFC Diagram": the chart as a real editor over the .sfc
 * document, exactly like the FBD/Ladder custom editors (text remains the
 * default). The view is a faithful projection: edit the text — including
 * through the text editor side by side — and the chart follows. */
export class SfcEditorProvider implements vscode.CustomTextEditorProvider {
  static readonly viewType = "nautilus.sfcDiagram";

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly live?: LiveValues
  ) {}

  register(): vscode.Disposable {
    return vscode.window.registerCustomEditorProvider(SfcEditorProvider.viewType, this, {
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
      debounce = setTimeout(() => void postSfcModel(panel.webview, document), DEBOUNCE_MS);
    });
    const messageSub = panel.webview.onDidReceiveMessage((msg: { type?: string; op?: unknown }) => {
      handleSfcMessage(document, msg);
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

    await postSfcModel(panel.webview, document);
  }
}

export class SfcPreview implements vscode.Disposable {
  private panel?: vscode.WebviewPanel;
  private docUri?: vscode.Uri;
  private debounce?: NodeJS.Timeout;
  /** Set while diffing: the frozen base source + title. Edits RE-DIFF
   * against it (the overlay tracks your changes live); the toolbar's
   * "exit diff" or reopening the preview leaves diff mode. */
  private diffBase?: { src: string; title: string; headSrc?: string };
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
        // Follow the active .sfc file, like the FBD/Ladder previews.
        if (this.panel && ed && ed.document.languageId === "iec-sfc") {
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
        if (!this.panel || !this.docUri) return;
        if (!e.uris.some((u) => u.toString() === this.docUri?.toString())) return;
        const doc = vscode.workspace.textDocuments.find((d) => d.uri.toString() === this.docUri?.toString());
        if (doc) postDiagnostics(this.panel.webview, doc);
      })
    );
  }

  /** The .sfc document the user is "in": active editor, active diagram tab,
   * the tracked one, or any open .sfc as a last resort. */
  private activeSfcDoc(): vscode.TextDocument | undefined {
    const ed = vscode.window.activeTextEditor?.document;
    if (ed && ed.languageId === "iec-sfc") return ed;
    const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
    if (input instanceof vscode.TabInputCustom && input.uri.path.toLowerCase().endsWith(".sfc")) {
      const custom = vscode.workspace.textDocuments.find(
        (d) => d.uri.toString() === (input as vscode.TabInputCustom).uri.toString()
      );
      if (custom) return custom;
    }
    const tracked = vscode.workspace.textDocuments.find(
      (d) => d.uri.toString() === this.docUri?.toString()
    );
    if (tracked) return tracked;
    return vscode.workspace.textDocuments.find((d) => d.languageId === "iec-sfc");
  }

  /** Open (or reveal) the SFC preview for the active .sfc editor. */
  async preview(): Promise<void> {
    const doc = this.activeSfcDoc();
    if (!doc) {
      void vscode.window.showErrorMessage("nautilus: open a .sfc file to preview its chart");
      return;
    }
    this.docUri = doc.uri;
    this.diffBase = undefined;
    this.ensurePanel();
    await this.update(doc);
  }

  /** Visual diff: the working tree (current buffer) vs git HEAD. */
  async diff(): Promise<void> {
    const doc = this.activeSfcDoc();
    if (!doc) {
      void vscode.window.showErrorMessage("nautilus: open a .sfc file first");
      return;
    }
    if (doc.uri.scheme !== "file") {
      void vscode.window.showErrorMessage("nautilus: SFC diff needs a file on disk");
      return;
    }
    const baseSrc = await gitShowHead(doc.uri.fsPath);
    if (baseSrc === undefined) {
      void vscode.window.showErrorMessage(
        `nautilus: ${docTitle(doc)} has no committed version (not in git HEAD)`
      );
      return;
    }
    await this.showDiff(doc, baseSrc, `${docTitle(doc)} — HEAD ↔ working tree`);
  }

  /** Visual diff: the working tree vs what the controller is running — the
   * controller serves each task's ORIGINAL source, so .sfc diffs as a
   * chart-vs-chart overlay, live. */
  async diffController(): Promise<void> {
    const doc = this.activeSfcDoc();
    if (!doc) {
      void vscode.window.showErrorMessage("nautilus: open a .sfc file first");
      return;
    }
    const info = await fetchControllerProgram(doc.getText());
    if (!info) return;
    if (info.language !== "sfc") {
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

  /** Visual diff between any two revisions of the file in git — or one
   * revision and the working tree. With two commits chosen both sides are
   * frozen, so edits leave the overlay alone. */
  async diffRevisions(): Promise<void> {
    const doc = this.activeSfcDoc();
    if (!doc) {
      void vscode.window.showErrorMessage("nautilus: open a .sfc file first");
      return;
    }
    if (doc.uri.scheme !== "file") {
      void vscode.window.showErrorMessage("nautilus: SFC diff needs a file on disk");
      return;
    }
    const pair = await pickRevisions(doc.uri.fsPath);
    if (!pair) return;
    await this.showDiff(
      doc,
      pair.base.src,
      `${docTitle(doc)} — ${pair.base.label} ↔ ${pair.head?.label ?? "working tree"}`,
      pair.head?.src
    );
  }

  /** Enter diff mode: freeze the base (and the head, when given) and post
   * the first overlay. */
  private async showDiff(
    doc: vscode.TextDocument,
    baseSrc: string,
    title: string,
    headSrc?: string
  ): Promise<void> {
    this.docUri = doc.uri;
    this.ensurePanel();
    this.diffBase = { src: baseSrc, title, headSrc };
    await this.postDiff(doc);
  }

  /** Graph the frozen base + the CURRENT text and post the overlay (the
   * webview's diffSfc does the actual base/head merge). */
  private async postDiff(doc: vscode.TextDocument): Promise<void> {
    if (!this.panel || !this.diffBase) return;
    const headSrc = this.diffBase.headSrc ?? doc.getText();
    const [base, head] = await Promise.all([sfcGraph(this.diffBase.src), sfcGraph(headSrc)]);
    if (base.error || head.error) {
      // Mid-edit the head may not parse for a moment — stay in diff mode,
      // surface the message, and the next edit re-diffs.
      void this.panel.webview.postMessage({
        type: "error",
        message: head.error ?? base.error,
        title: docTitle(doc),
        lang: "sfc",
      });
      return;
    }
    this.panel.title = "SFC: diff";
    void this.panel.webview.postMessage({
      type: "sfcDiff",
      base: base.model,
      head: head.model,
      title: this.diffBase.title,
    });
  }

  private ensurePanel(): void {
    if (!this.panel) {
      this.panel = vscode.window.createWebviewPanel(
        "nautilus.sfcPreview",
        "SFC",
        vscode.ViewColumn.Beside,
        // Like FBD's: selection, clipboard and scroll survive a tab switch.
        { ...webviewOptions(this.context.extensionUri), retainContextWhenHidden: true }
      );
      this.panel.webview.html = buildWebviewHtml(this.panel.webview, this.context.extensionUri, {
        forwardKeys: true,
      });
      this.panel.webview.onDidReceiveMessage((msg: { type?: string; op?: unknown }) => {
        const doc = vscode.workspace.textDocuments.find(
          (d) => d.uri.toString() === this.docUri?.toString()
        );
        if (isDiagramKeyMessage(msg)) {
          if (doc && this.panel) {
            void applyDiagramKey(msg.action, doc, this.panel, { diffing: this.diffBase !== undefined });
          }
          return;
        }
        if (msg?.type === "exitDiff") {
          this.diffBase = undefined;
          if (doc) void this.update(doc);
          return;
        }
        if (doc) handleSfcMessage(doc, msg);
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
      if (this.diffBase.headSrc === undefined) await this.postDiff(doc);
      return;
    }
    this.panel.title = "SFC: " + docTitle(doc);
    await postSfcModel(this.panel.webview, doc);
  }

  dispose(): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.panel?.dispose();
    for (const d of this.disposables) d.dispose();
  }
}
