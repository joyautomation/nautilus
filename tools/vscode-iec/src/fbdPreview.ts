// FBD diagram preview: a webview panel that renders a .fbd file's diagram,
// live-updating as the text changes. The text is the source of truth; the
// diagram is a projection. The render model comes from `nautilus fbd graph -`
// (the same CLI that provides the language server), so the FBD parser lives
// in exactly one place — Go. The webview (media/fbd.js) only does geometry.
//
// Also provides the visual diff: two render models (git HEAD vs the working
// tree) posted together; the webview overlays them and colors nodes/edges
// added / removed / changed. Node ids are stable diff keys by construction
// (they derive from wire/instance/coil names + argument position, never from
// statement order or layout).

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
export type FbdSpan = { line: number; col: number; endLine?: number; endCol?: number; text?: string };
export type FbdEdge = {
  from: string;
  fromPin?: string;
  to: string;
  toPin?: string;
  wire?: string;
  negated?: boolean;
  feedback?: boolean;
  arg?: FbdSpan;
  not?: FbdSpan;
  inner?: FbdSpan;
};

/** Edit gestures the webview can send back; each maps to a text edit
 * anchored by source spans from the render model. */
type EditMessage =
  | { type: "editLiteral"; span: FbdSpan; newText: string }
  | { type: "toggleNot"; arg: FbdSpan; not?: FbdSpan | null; inner?: FbdSpan | null }
  | { type: "rewire"; arg: FbdSpan; newText: string }
  | { type: "insertTemplate"; snippet: string };

const DEBOUNCE_MS = 150;

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
    await this.showDiff(doc, baseSrc, "in git HEAD", `${this.title(doc)} — HEAD ↔ working tree`);
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
      `${this.title(doc)} — controller ${info.hash}${info.dirty ? " · online edit" : ""} ↔ workspace`
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
    const [base, head] = await Promise.all([this.graph(baseSrc), this.graph(doc.getText())]);
    if ("error" in base || "error" in head) {
      const msg = ("error" in head ? head.error : "") || ("error" in base ? `${baseLabel}: ${base.error}` : "");
      this.post({ type: "error", message: msg, title: this.title(doc) });
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

  private title(doc: vscode.TextDocument): string {
    return path.basename(doc.uri.fsPath || doc.uri.path);
  }

  private scheduleUpdate(doc: vscode.TextDocument): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.debounce = setTimeout(() => void this.update(doc), DEBOUNCE_MS);
  }

  private async update(doc: vscode.TextDocument): Promise<void> {
    if (!this.panel) return;
    const res = await this.graph(doc.getText());
    if ("error" in res) {
      this.post({ type: "error", message: res.error, title: this.title(doc) });
    } else {
      this.post({ type: "model", model: res.model, title: this.title(doc) });
    }
  }

  /** Run `nautilus fbd graph -` over source text. */
  private graph(source: string): Promise<{ model: FbdModel } | { error: string }> {
    const cliPath = vscode.workspace.getConfiguration("nautilus").get<string>("cliPath", "nautilus");
    return new Promise((resolve) => {
      const child = execFile(
        cliPath,
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
            return resolve({
              error:
                `Couldn't run "${cliPath}". Install the nautilus CLI:\n` +
                "go install github.com/joyautomation/nautilus/cmd/nautilus@latest",
            });
          }
          resolve({ error: err ? String(err) : "nautilus fbd graph: empty output" });
        }
      );
      child.stdin?.end(source);
    });
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
      {
        enableScripts: true,
        localResourceRoots: [vscode.Uri.joinPath(this.context.extensionUri, "media")],
        retainContextWhenHidden: true,
      }
    );
    this.panel.onDidDispose(() => {
      this.panel = undefined;
      this.diffing = false;
    });
    this.panel.webview.onDidReceiveMessage((msg: EditMessage) => void this.applyEdit(msg));
    const webview = this.panel.webview;
    const scriptUri = webview.asWebviewUri(
      vscode.Uri.joinPath(this.context.extensionUri, "media", "fbd.js")
    );
    const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
    webview.html = /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>FBD Preview</title>
</head>
<body>
<div id="app"></div>
<script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
  }

  private post(msg: unknown): void {
    if (!this.panel) return;
    this.panel.title = "FBD: " + ((msg as { title?: string }).title ?? "Preview");
    void this.panel.webview.postMessage(msg);
  }

  /** Apply a diagram gesture as a text edit to the tracked .fbd document.
   * Every edit is span-anchored and verified against the current text before
   * it applies — if the document moved under us (or a literal's rendered
   * form differs from the source), nothing is touched. The text edit then
   * round-trips through the normal change → re-render path, so the diagram
   * the user sees is always a projection of real text. */
  private async applyEdit(msg: EditMessage): Promise<void> {
    if (this.diffing || !this.docUri) return;
    const doc = vscode.workspace.textDocuments.find(
      (d) => d.uri.toString() === this.docUri?.toString()
    );
    if (!doc) return;
    const at = (s: FbdSpan) => new vscode.Position(s.line - 1, s.col - 1);
    const verify = (s: FbdSpan): boolean => {
      if (!s.text) return true;
      const range = new vscode.Range(at(s), at(s).translate(0, s.text.length));
      return doc.getText(range).toUpperCase() === s.text.toUpperCase();
    };
    const edit = new vscode.WorkspaceEdit();
    if (msg.type === "editLiteral") {
      const newText = msg.newText.trim();
      if (!newText || !msg.span.text || newText === msg.span.text) return;
      if (!verify(msg.span)) {
        void vscode.window.showWarningMessage(
          "nautilus: couldn't locate that constant in the source — edit the text directly"
        );
        return;
      }
      edit.replace(
        doc.uri,
        new vscode.Range(at(msg.span), at(msg.span).translate(0, msg.span.text.length)),
        newText
      );
    } else if (msg.type === "toggleNot") {
      if (msg.not && msg.inner) {
        // Negated: delete [NOT, operand) — the keyword and its whitespace.
        if (!verify(msg.not)) {
          void vscode.window.showWarningMessage(
            "nautilus: couldn't locate the NOT in the source — edit the text directly"
          );
          return;
        }
        edit.delete(doc.uri, new vscode.Range(at(msg.not), at(msg.inner)));
      } else {
        edit.insert(doc.uri, at(msg.arg), "NOT ");
      }
    } else if (msg.type === "rewire") {
      // Replace the whole argument expression with a reference to the
      // dragged source. The span carries the exact source text — verify it
      // (case-sensitive: we're replacing the real bytes) before touching.
      const a = msg.arg;
      if (!a.text || a.endLine === undefined || a.endCol === undefined) return;
      const range = new vscode.Range(at(a), new vscode.Position(a.endLine - 1, a.endCol - 1));
      if (doc.getText(range) !== a.text) {
        void vscode.window.showWarningMessage(
          "nautilus: couldn't locate that connection in the source — edit the text directly"
        );
        return;
      }
      edit.replace(doc.uri, range, msg.newText);
    } else if (msg.type === "insertTemplate") {
      // Drop a snippet just above END_FBD and hand focus to the text editor
      // with the tabstops live — the diagram re-renders as they're filled.
      // Target the group where the file is ALREADY visible (or group one),
      // never the preview's own group — otherwise a duplicate tab opens on
      // top of the diagram.
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
      return;
    }
    await vscode.workspace.applyEdit(edit);
  }

  dispose(): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.panel?.dispose();
    for (const d of this.disposables) d.dispose();
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
