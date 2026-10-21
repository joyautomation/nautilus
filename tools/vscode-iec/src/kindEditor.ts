// Kind editor: one editor per 3D kind (docs/design/spatial-hmi.md §3g,
// slice 2). A `{Name}.kind.json` — a scene's `kinds` entry in a file of
// its own: type, members, model, bounds, labelAt, status, drive — opens as
// the kind itself: its model on a turntable in the hmi-3d renderer, the
// model's mesh names listed so a drive targets a real one, sample member
// values driving the meshes and the status text, and the fields beside
// it. Same shape as the scene editor (sceneEditor.ts): the webview sends
// ops, the host applies each as one WorkspaceEdit (kindOps.ts), text undo
// covers the session, and a hand edit beside it re-renders live. The
// scene editor's webview bundle is reused with a different mount
// (webview-ui/src/scene/main.ts reads <body data-scene-mode>), the way
// the mimic bundle hosts the component editor.

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import { webviewOptions } from "./fbdPreview";
import { applyKindOp, parseKind, type KindOp } from "./kindOps";
import { kindNameFromFilename, resolveKindModel } from "./kindEditorLogic";
import { reopenAsText } from "./mimicEditor";

const DEBOUNCE_MS = 150;

let kindLog: vscode.OutputChannel | undefined;
function logKind(msg: string): void {
  kindLog ??= vscode.window.createOutputChannel("nautilus kind");
  kindLog.appendLine(`[${new Date().toISOString()}] ${msg}`);
}

function kindHtml(webview: vscode.Webview, extensionUri: vscode.Uri): string {
  const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "scene-editor.js"));
  const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "scene-editor.css"));
  const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}'; img-src ${webview.cspSource} blob: data:; connect-src ${webview.cspSource} blob: data:; worker-src blob:;">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="stylesheet" href="${styleUri}">
<title>Kind</title>
</head>
<body data-scene-mode="kind">
<div id="app"></div>
<script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
}

/** "Open With → Kind Editor" (the DEFAULT for *.kind.json). */
export class KindEditorProvider implements vscode.CustomTextEditorProvider {
  static readonly viewType = "nautilus.kindEditor";

  private opQueue: Promise<void> = Promise.resolve();

  constructor(private readonly context: vscode.ExtensionContext) {}

  register(): vscode.Disposable {
    return vscode.window.registerCustomEditorProvider(KindEditorProvider.viewType, this, {
      webviewOptions: { retainContextWhenHidden: true },
      supportsMultipleEditorsPerDocument: true,
    });
  }

  async resolveCustomTextEditor(document: vscode.TextDocument, panel: vscode.WebviewPanel): Promise<void> {
    const kindDir = path.dirname(document.uri.fsPath);
    const roots = [...(vscode.workspace.workspaceFolders ?? []).map((f) => f.uri), vscode.Uri.file(kindDir)];
    panel.webview.options = webviewOptions(this.context.extensionUri, roots);
    panel.webview.html = kindHtml(panel.webview, this.context.extensionUri);

    const exists = (file: string) => {
      try {
        return fs.statSync(file).isFile();
      } catch {
        return false;
      }
    };

    const postDoc = () => {
      try {
        const kind = parseKind(document.getText());
        const { kind: rendered, model } = resolveKindModel(kind as Record<string, unknown>, kindDir, exists, (f) =>
          panel.webview.asWebviewUri(vscode.Uri.file(f)).toString()
        );
        void panel.webview.postMessage({
          type: "kindDoc",
          kind,
          rendered,
          model,
          name: kindNameFromFilename(document.uri.fsPath),
          title: document.uri.path.split("/").pop() ?? "kind",
        });
      } catch (err) {
        void panel.webview.postMessage({ type: "kindError", message: err instanceof Error ? err.message : String(err) });
      }
    };

    let debounce: NodeJS.Timeout | undefined;
    const changeSub = vscode.workspace.onDidChangeTextDocument((e) => {
      if (e.document.uri.toString() !== document.uri.toString()) return;
      if (debounce) clearTimeout(debounce);
      debounce = setTimeout(postDoc, DEBOUNCE_MS);
    });

    const messageSub = panel.webview.onDidReceiveMessage((msg: { type?: string; op?: KindOp; msg?: unknown }) => {
      switch (msg?.type) {
        case "kindReady":
          postDoc();
          return;
        case "kindTrace":
          logKind("webview: " + String(msg.msg ?? ""));
          return;
        case "kindError": {
          const text = String(msg.msg ?? "");
          logKind("webview error: " + text);
          void vscode.window.showWarningMessage("nautilus: " + text);
          return;
        }
        case "reopenAsText":
          reopenAsText(document.uri);
          return;
        case "kindOp": {
          const op = msg.op;
          if (!op) return;
          this.opQueue = this.opQueue
            .then(async () => {
              logKind("op: " + JSON.stringify(op));
              const res = applyKindOp(document.getText(), op);
              if ("error" in res) {
                logKind("  -> refused: " + res.error);
                void vscode.window.showWarningMessage("nautilus: " + res.error);
                return;
              }
              if (res.text === document.getText()) {
                logKind("  -> no change");
                return;
              }
              const edit = new vscode.WorkspaceEdit();
              edit.replace(document.uri, new vscode.Range(0, 0, document.lineCount, 0), res.text);
              const ok = await vscode.workspace.applyEdit(edit);
              logKind(`  -> applied=${ok}`);
              if (ok) postDoc();
            })
            .catch((err) => logKind("  -> exception: " + String(err)));
          return;
        }
      }
    });

    panel.onDidDispose(() => {
      if (debounce) clearTimeout(debounce);
      changeSub.dispose();
      messageSub.dispose();
    });

    postDoc();
  }
}
