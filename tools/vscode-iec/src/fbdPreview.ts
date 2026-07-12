// FBD diagram preview + editor: webviews that render a .fbd file's diagram,
// live-updating as the text changes. The text is the source of truth; the
// diagram is a projection. The render model comes from `nautilus fbd graph -`
// and every edit gesture becomes a STRUCTURAL OP (`nautilus fbd edit`):
// the op is addressed by stable render-model ids, resolved in Go against a
// fresh parse of the current buffer, and comes back as minimal text edits —
// no consumer of the model ever computes source spans itself.
//
// Two hosts share this logic: the preview command's singleton panel (opens
// beside the text, follows the active .fbd editor, also renders diffs) and
// the CustomTextEditor ("Open With → FBD Diagram"), which ties a diagram
// per-document into VS Code's editor lifecycle.

import * as vscode from "vscode";
import { execFile } from "child_process";
import * as path from "path";
import type { ProgramInfo } from "./onlineEdit";

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
  type: "setLiteral" | "toggleNot" | "rewire" | "rename" | "deleteNode";
  node?: string;
  to?: string;
  toPin?: string;
  from?: string;
  fromPin?: string;
  value?: string;
  newName?: string;
  source?: string;
  sourcePin?: string;
};

/** Mirror of lang/fbd.TextEdit: 1-based, end-exclusive. */
type FbdTextEdit = { line: number; col: number; endLine: number; endCol: number; newText: string };

type WebviewMessage =
  | { type: "edit"; op: FbdEditOp }
  | { type: "insertTemplate"; snippet: string };

const DEBOUNCE_MS = 150;

// ── CLI seam ───────────────────────────────────────────────────────────────

function cliPath(): string {
  return vscode.workspace.getConfiguration("nautilus").get<string>("cliPath", "nautilus");
}

/** Run `nautilus fbd graph -` over source text. */
export function fbdGraph(source: string): Promise<{ model: FbdModel } | { error: string }> {
  const cli = cliPath();
  return new Promise((resolve) => {
    const child = execFile(
      cli,
      ["fbd", "graph", "-"],
      { timeout: 10_000, maxBuffer: 16 * 1024 * 1024 },
      (err, stdout) => {
        // Exit 1 still writes {"error": ...} JSON on stdout — prefer it.
        try {
          const parsed = JSON.parse(stdout) as FbdModel & { error?: string };
          if (parsed.error) return resolve({ error: parsed.error });
          return resolve({ model: parsed });
        } catch {
          /* fall through */
        }
        if (err && (err as NodeJS.ErrnoException).code === "ENOENT") {
          return resolve({ error: cliMissing(cli) });
        }
        resolve({ error: err ? String(err) : "nautilus fbd graph: empty output" });
      }
    );
    child.stdin?.end(source);
  });
}

/** Run `nautilus fbd edit`: resolve op against source, get minimal edits. */
function fbdEdit(source: string, op: FbdEditOp): Promise<{ edits: FbdTextEdit[] } | { error: string }> {
  const cli = cliPath();
  return new Promise((resolve) => {
    const child = execFile(
      cli,
      ["fbd", "edit"],
      { timeout: 10_000, maxBuffer: 16 * 1024 * 1024 },
      (err, stdout) => {
        try {
          const parsed = JSON.parse(stdout) as { edits?: FbdTextEdit[]; error?: string };
          if (parsed.error) return resolve({ error: parsed.error });
          return resolve({ edits: parsed.edits ?? [] });
        } catch {
          /* fall through */
        }
        if (err && (err as NodeJS.ErrnoException).code === "ENOENT") {
          return resolve({ error: cliMissing(cli) });
        }
        resolve({ error: err ? String(err) : "nautilus fbd edit: empty output" });
      }
    );
    child.stdin?.end(JSON.stringify({ source, op }));
  });
}

function cliMissing(cli: string): string {
  return (
    `Couldn't run "${cli}". Install the nautilus CLI:\n` +
    "go install github.com/joyautomation/nautilus/cmd/nautilus@latest"
  );
}

// ── shared webview session logic ───────────────────────────────────────────

function buildWebviewHtml(webview: vscode.Webview, extensionUri: vscode.Uri): string {
  const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "fbd.js"));
  const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>FBD</title>
</head>
<body>
<div id="app"></div>
<script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
}

function webviewOptions(extensionUri: vscode.Uri): vscode.WebviewOptions {
  return {
    enableScripts: true,
    localResourceRoots: [vscode.Uri.joinPath(extensionUri, "media")],
  };
}

/** Handle a gesture from the webview against a specific document. Ops go
 * through `nautilus fbd edit` — Go resolves them on the CURRENT buffer text
 * and returns minimal edits, so a moved buffer can't misfire; a rejected op
 * (used wire deleted, name collision, …) surfaces its reason. */
async function handleWebviewMessage(doc: vscode.TextDocument, msg: WebviewMessage): Promise<void> {
  if (msg.type === "edit") {
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
    return;
  }
  if (msg.type === "insertTemplate") {
    // Drop a snippet just above END_FBD and hand focus to the text editor
    // with the tabstops live — the diagram re-renders as they're filled.
    // Target the group where the file is ALREADY visible (or group one),
    // never the diagram's own group — otherwise a duplicate tab opens on
    // top of it.
    for (let i = doc.lineCount - 1; i >= 0; i--) {
      if (/^\s*END_FBD\s*$/i.test(doc.lineAt(i).text)) {
        const visible = vscode.window.visibleTextEditors.find(
          (e) => e.document.uri.toString() === doc.uri.toString()
        );
        const editor = await vscode.window.showTextDocument(doc, {
          viewColumn: visible?.viewColumn ?? vscode.ViewColumn.One,
          preview: false,
        });
        await editor.insertSnippet(
          new vscode.SnippetString("  " + msg.snippet + "\n"),
          new vscode.Position(i, 0)
        );
        return;
      }
    }
    void vscode.window.showWarningMessage("nautilus: no END_FBD found to insert before");
  }
}

function docTitle(doc: vscode.TextDocument): string {
  return path.basename(doc.uri.fsPath || doc.uri.path);
}

async function postModel(webview: vscode.Webview, doc: vscode.TextDocument): Promise<void> {
  const res = await fbdGraph(doc.getText());
  if ("error" in res) {
    void webview.postMessage({ type: "error", message: res.error, title: docTitle(doc) });
  } else {
    void webview.postMessage({ type: "model", model: res.model, title: docTitle(doc) });
  }
}

// ── the preview command's singleton panel ──────────────────────────────────

export class FbdPreview implements vscode.Disposable {
  private panel?: vscode.WebviewPanel;
  private docUri?: vscode.Uri;
  private debounce?: NodeJS.Timeout;
  private disposables: vscode.Disposable[] = [];
  /** Set while showing a diff; live edits leave the diff on screen. */
  private diffing = false;

  constructor(private readonly context: vscode.ExtensionContext) {
    this.disposables.push(
      vscode.workspace.onDidChangeTextDocument((e) => {
        if (this.panel && !this.diffing && e.document.uri.toString() === this.docUri?.toString()) {
          this.scheduleUpdate(e.document);
        }
      }),
      vscode.window.onDidChangeActiveTextEditor((ed) => {
        // Follow the active .fbd file, like the markdown preview.
        if (this.panel && ed && ed.document.languageId === "iec-fbd") {
          this.docUri = ed.document.uri;
          this.diffing = false;
          this.scheduleUpdate(ed.document);
        }
      })
    );
  }

  /** Open (or reveal) the preview panel for the active .fbd editor. */
  async preview(): Promise<void> {
    const doc = this.activeFbdDoc();
    if (!doc) return;
    this.docUri = doc.uri;
    this.diffing = false;
    this.ensurePanel();
    await this.update(doc);
  }

  /** Visual diff: the working tree (current buffer) vs git HEAD. */
  async diff(): Promise<void> {
    const doc = this.activeFbdDoc();
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
    const doc = this.activeFbdDoc();
    if (!doc) return;
    const url = vscode.workspace
      .getConfiguration("nautilus")
      .get<string>("runtimeUrl", "http://localhost:8080")
      .replace(/\/+$/, "");
    let info: ProgramInfo;
    try {
      const res = await fetch(url + "/api/program");
      if (!res.ok) throw new Error(res.statusText);
      info = (await res.json()) as ProgramInfo;
    } catch {
      void vscode.window.showErrorMessage(`nautilus: no controller at ${url}`);
      return;
    }
    if (info.language !== "fbd") {
      void vscode.window.showErrorMessage(
        "nautilus: the controller is running an ST program — use \"Diff Program with Controller\" for the text diff"
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

  /** Graph base + head sources and post the overlay to the webview. */
  private async showDiff(
    doc: vscode.TextDocument,
    baseSrc: string,
    baseLabel: string,
    title: string
  ): Promise<void> {
    this.docUri = doc.uri;
    this.ensurePanel();
    const [base, head] = await Promise.all([fbdGraph(baseSrc), fbdGraph(doc.getText())]);
    if ("error" in base || "error" in head) {
      const msg = ("error" in head ? head.error : "") || ("error" in base ? `${baseLabel}: ${base.error}` : "");
      this.post({ type: "error", message: msg, title: docTitle(doc) });
      return;
    }
    this.diffing = true;
    this.post({ type: "diff", base: base.model, head: head.model, title });
  }

  private activeFbdDoc(): vscode.TextDocument | undefined {
    const doc = vscode.window.activeTextEditor?.document;
    if (doc && doc.languageId === "iec-fbd") return doc;
    // The preview panel may have focus; fall back to the tracked document.
    const tracked = vscode.workspace.textDocuments.find(
      (d) => d.uri.toString() === this.docUri?.toString()
    );
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
    const res = await fbdGraph(doc.getText());
    if ("error" in res) {
      this.post({ type: "error", message: res.error, title: docTitle(doc) });
    } else {
      this.post({ type: "model", model: res.model, title: docTitle(doc) });
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
      this.diffing = false;
    });
    this.panel.webview.onDidReceiveMessage((msg: WebviewMessage) => {
      if (this.diffing || !this.docUri) return;
      const doc = vscode.workspace.textDocuments.find(
        (d) => d.uri.toString() === this.docUri?.toString()
      );
      if (doc) void handleWebviewMessage(doc, msg);
    });
    this.panel.webview.html = buildWebviewHtml(this.panel.webview, this.context.extensionUri);
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

  constructor(private readonly context: vscode.ExtensionContext) {}

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
    panel.onDidDispose(() => {
      if (debounce) clearTimeout(debounce);
      changeSub.dispose();
      messageSub.dispose();
    });

    await postModel(panel.webview, document);
  }
}

/** The file's content at git HEAD, or undefined if untracked/not a repo. */
function gitShowHead(fsPath: string): Promise<string | undefined> {
  const dir = path.dirname(fsPath);
  const base = path.basename(fsPath);
  return new Promise((resolve) => {
    // "./" makes the path relative to cwd rather than the repo root.
    execFile(
      "git",
      ["show", `HEAD:./${base}`],
      { cwd: dir, maxBuffer: 16 * 1024 * 1024 },
      (err, stdout) => resolve(err ? undefined : stdout)
    );
  });
}
