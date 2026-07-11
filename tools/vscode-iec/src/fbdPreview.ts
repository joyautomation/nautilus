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
    this.docUri = doc.uri;
    this.ensurePanel();
    const [base, head] = await Promise.all([this.graph(baseSrc), this.graph(doc.getText())]);
    if ("error" in base || "error" in head) {
      const msg = ("error" in head ? head.error : "") || ("error" in base ? `in git HEAD: ${base.error}` : "");
      this.post({ type: "error", message: msg, title: this.title(doc) });
      return;
    }
    this.diffing = true;
    this.post({
      type: "diff",
      base: base.model,
      head: head.model,
      title: `${this.title(doc)} — HEAD ↔ working tree`,
    });
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
