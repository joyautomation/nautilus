// Component editor: a *.component.json sidecar (see mimicComponents.ts/
// mimicComponentIndex.ts) opens as a small graphical editor — one
// component, centered, with its connection points as draggable dots —
// instead of raw JSON. Same shape as the mimic editor (op queue -> whole-
// document WorkspaceEdit -> text undo covers the session; a
// ready-handshake because a one-shot postMessage can beat the webview's
// listener registration), reusing its webview bundle with a different
// mount (see webview-ui/src/mimic/main.ts's data-mimic-mode).
//
// Today the editor only shows/edits `ports` — the sidecar's one implemented
// key — but it's named for the FILE (a general per-component metadata
// sidecar), not the one thing it currently edits, since that's the whole
// point of the sidecar: future keys (defaults, bindHints, palette, ...)
// grow this same editor rather than needing a new one.
//
// The component name is the filename minus ".component.json" — not read
// from the JSON, so renaming the file IS renaming which component it
// overrides.

import * as vscode from "vscode";
import { webviewOptions } from "./fbdPreview";
import {
  componentNameFromFilename,
  formatComponentEntry,
  parseComponentEntryStrict,
  patchComponentPortsText,
  validatePortList,
  type Port,
} from "./mimicComponentIndex";
import { reopenAsText } from "./mimicEditor";
import { userComponentScriptTag, type UserComponentManager } from "./userComponents";

const DEBOUNCE_MS = 150;

function componentHtml(webview: vscode.Webview, extensionUri: vscode.Uri, userComponents: UserComponentManager): string {
  // Same bundle as the mimic editor — the JS decides what to mount from
  // data-mimic-mode, so a second vite config/output isn't needed for what
  // is, gesture-wise, a small variant of the same canvas.
  const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "mimic-editor.js"));
  const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "mimic-editor.css"));
  const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
  const userScript = userComponentScriptTag(webview, userComponents, nonce);
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="stylesheet" href="${styleUri}">
<title>Component</title>
</head>
<body data-mimic-mode="component">
<div id="app"></div>
<script nonce="${nonce}" src="${scriptUri}"></script>${userScript}
</body>
</html>`;
}

/** "Open With → Component Editor" (the DEFAULT for *.component.json). */
export class ComponentEditorProvider implements vscode.CustomTextEditorProvider {
  static readonly viewType = "nautilus.componentEditor";

  /** Every gesture rewrites the whole entry, applied as one WorkspaceEdit —
   * same ordering rule as the mimic/FBD/ladder queues: each must read the
   * text AFTER the previous edit landed. */
  private opQueue: Promise<void> = Promise.resolve();

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly userComponents: UserComponentManager
  ) {}

  register(): vscode.Disposable {
    return vscode.window.registerCustomEditorProvider(ComponentEditorProvider.viewType, this, {
      webviewOptions: { retainContextWhenHidden: true },
      supportsMultipleEditorsPerDocument: true,
    });
  }

  async resolveCustomTextEditor(document: vscode.TextDocument, panel: vscode.WebviewPanel): Promise<void> {
    panel.webview.options = webviewOptions(this.context.extensionUri, [this.userComponents.resourceRoot]);
    panel.webview.html = componentHtml(panel.webview, this.context.extensionUri, this.userComponents);
    this.userComponents.registerPanel(panel);

    const filename = document.uri.path.split("/").pop() ?? "component.json";
    const component = componentNameFromFilename(filename) ?? filename.replace(/\.json$/, "");
    // A sidecar names exactly one component — request() itself filters out
    // built-ins, so this is harmless even when `component` turns out to be one.
    this.userComponents.request(panel, [component]);

    const postUserComponentDiagnostics = () =>
      void panel.webview.postMessage({
        type: "userComponentDiagnostics",
        diagnostics: this.userComponents.lastDiagnostics(),
      });

    const postDoc = () => {
      try {
        const entry = parseComponentEntryStrict(document.getText());
        void panel.webview.postMessage({
          type: "componentDoc",
          component,
          ports: entry.ports ?? [],
          title: filename,
        });
      } catch (err) {
        void panel.webview.postMessage({
          type: "componentError",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    };

    let debounce: NodeJS.Timeout | undefined;
    const changeSub = vscode.workspace.onDidChangeTextDocument((e) => {
      if (e.document.uri.toString() !== document.uri.toString()) return;
      if (debounce) clearTimeout(debounce);
      debounce = setTimeout(postDoc, DEBOUNCE_MS);
    });

    // A rebuilt bundle needs a fresh <script src> — reassigning webview.html
    // reloads the page; componentReady below re-answers once it remounts.
    const userComponentsSub = this.userComponents.onDidChange(() => {
      panel.webview.html = componentHtml(panel.webview, this.context.extensionUri, this.userComponents);
      postUserComponentDiagnostics();
    });

    const messageSub = panel.webview.onDidReceiveMessage(
      (msg: { type?: string; ports?: Port[] }) => {
        // Same handshake as the mimic editor: the unsolicited postDoc below
        // can beat the webview's mount and be dropped silently.
        if (msg?.type === "componentReady") {
          postDoc();
          postUserComponentDiagnostics();
          return;
        }
        if (msg?.type === "reopenAsText") {
          reopenAsText(document.uri);
          return;
        }
        if (msg?.type !== "componentOp" || !validatePortList(msg.ports)) return;
        this.opQueue = this.opQueue
          .then(async () => {
            // Patch just `ports` — any other (future-metadata) keys already
            // in the file pass through untouched. If the text doesn't parse
            // (a side-by-side hand edit mid-keystroke), REFUSE: replacing it
            // with `{ports}` would silently throw the hand edit away.
            const res = patchComponentPortsText(document.getText(), msg.ports!);
            if ("error" in res) {
              void vscode.window.showWarningMessage(`nautilus: ${filename}: ${res.error}`);
              postDoc();
              return;
            }
            const text = res.text ?? formatComponentEntry({});
            if (text === document.getText()) return;
            const edit = new vscode.WorkspaceEdit();
            edit.replace(document.uri, new vscode.Range(0, 0, document.lineCount, 0), text);
            const ok = await vscode.workspace.applyEdit(edit);
            if (ok) postDoc();
          })
          .catch(() => {
            /* best-effort — the change-event debounce will resync the webview */
          });
        return;
      }
    );

    panel.onDidDispose(() => {
      if (debounce) clearTimeout(debounce);
      changeSub.dispose();
      messageSub.dispose();
      userComponentsSub.dispose();
    });

    postDoc();
  }
}
