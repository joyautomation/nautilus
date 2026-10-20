// Scene editor: the mimic editor's spatial twin. A *.scene.json document
// opens as the 3D view itself — the same renderer the built HMI uses
// (@joyautomation/nautilus-hmi-3d, bundled into the scene-editor webview)
// with a palette of the document's kinds, a gizmo on the selected node and
// a panel of its fields — while the text stays canonical: every gesture
// becomes an op (sceneOps.ts), applied as one WorkspaceEdit so text
// undo/redo covers the whole session, and editing the JSON beside it
// updates the view live. This is the loop docs/design/spatial-hmi.md §3g
// describes: an AI (or `naut scene init`) writes the document, a person
// drags what is in the wrong place, and the diff shows exactly the drag.
//
// When a controller is reachable (nautilus.runtimeUrl) and live values are
// on, the editor polls GET /api/state the way the mimic editor does, so a
// bound node animates with the real process while you place it.

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import { webviewOptions } from "./fbdPreview";
import { applySceneOp, parseScene, type SceneOp } from "./sceneOps";
import { paletteKinds } from "./sceneKinds";
import { resolveSceneAssets } from "./sceneEditorLogic";
import { fetchTags, liveEnabled, reopenAsText } from "./mimicEditor";

const DEBOUNCE_MS = 150;
const TAG_POLL_MS = 2000;

let sceneLog: vscode.OutputChannel | undefined;
function logScene(msg: string): void {
  sceneLog ??= vscode.window.createOutputChannel("nautilus scene");
  sceneLog.appendLine(`[${new Date().toISOString()}] ${msg}`);
}

function sceneHtml(webview: vscode.Webview, extensionUri: vscode.Uri): string {
  const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "scene-editor.js"));
  const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "scene-editor.css"));
  const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
  // img-src / connect-src: three fetches models, textures and HDRIs from the
  // workspace through asWebviewUri (cspSource), and decodes textures through
  // blob: / data: URLs. worker-src: three's loaders may decode off-thread.
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}'; img-src ${webview.cspSource} blob: data:; connect-src ${webview.cspSource} blob: data:; worker-src blob:;">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="stylesheet" href="${styleUri}">
<title>Scene</title>
</head>
<body>
<div id="app"></div>
<script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
}

function snapConfig(): boolean {
  return vscode.workspace.getConfiguration("nautilus").get<boolean>("scene.snapToGrid", true);
}

function setSnapConfig(value: boolean): void {
  const target = vscode.workspace.workspaceFolders?.length ? vscode.ConfigurationTarget.Workspace : vscode.ConfigurationTarget.Global;
  void vscode.workspace.getConfiguration("nautilus").update("scene.snapToGrid", value, target);
}

/** "Open With → Scene Editor" (the DEFAULT for *.scene.json — a scene is
 * spatial-first; the JSON is a right-click "Open With → Text Editor" away). */
export class SceneEditorProvider implements vscode.CustomTextEditorProvider {
  static readonly viewType = "nautilus.sceneEditor";

  /** Ops apply strictly in order: each must read the text AFTER the
   * previous edit landed (same rule as the mimic and diagram queues). */
  private opQueue: Promise<void> = Promise.resolve();

  constructor(private readonly context: vscode.ExtensionContext) {}

  register(): vscode.Disposable {
    return vscode.window.registerCustomEditorProvider(SceneEditorProvider.viewType, this, {
      webviewOptions: { retainContextWhenHidden: true },
      supportsMultipleEditorsPerDocument: true,
    });
  }

  async resolveCustomTextEditor(document: vscode.TextDocument, panel: vscode.WebviewPanel): Promise<void> {
    const sceneDir = path.dirname(document.uri.fsPath);
    // The webview fetches the scene's models and textures straight from
    // the workspace: every folder, plus the scene's own directory for a
    // file opened from outside one.
    const roots = [...(vscode.workspace.workspaceFolders ?? []).map((f) => f.uri), vscode.Uri.file(sceneDir)];
    panel.webview.options = webviewOptions(this.context.extensionUri, roots);
    panel.webview.html = sceneHtml(panel.webview, this.context.extensionUri);

    const exists = (file: string) => {
      try {
        return fs.statSync(file).isFile();
      } catch {
        return false;
      }
    };

    const postDoc = () => {
      try {
        const doc = parseScene(document.getText());
        const { doc: rendered, assets } = resolveSceneAssets(doc, sceneDir, exists, (f) =>
          panel.webview.asWebviewUri(vscode.Uri.file(f)).toString()
        );
        void panel.webview.postMessage({
          type: "sceneDoc",
          doc,
          rendered,
          assets,
          kinds: paletteKinds(doc),
          title: document.uri.path.split("/").pop() ?? "scene",
        });
      } catch (err) {
        void panel.webview.postMessage({
          type: "sceneError",
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

    const postConfig = () => void panel.webview.postMessage({ type: "sceneConfig", snapToGrid: snapConfig() });
    const configSub = vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("nautilus.scene.snapToGrid")) postConfig();
      if (e.affectsConfiguration("nautilus.liveValues.enabled") || e.affectsConfiguration("nautilus.runtimeUrl")) void postTags();
    });

    const postTags = async () => {
      const enabled = liveEnabled();
      const tags = enabled ? await fetchTags() : null;
      void panel.webview.postMessage({ type: "sceneTags", tags, enabled });
    };
    const tagTimer = setInterval(() => {
      if (panel.visible && liveEnabled()) void postTags();
    }, TAG_POLL_MS);

    const messageSub = panel.webview.onDidReceiveMessage((msg: { type?: string; op?: SceneOp; msg?: unknown; value?: unknown }) => {
      switch (msg?.type) {
        // The webview announces when its listener is up; answer with the
        // current state (the unsolicited postDoc below can beat its mount).
        case "sceneReady":
          postDoc();
          postConfig();
          void postTags();
          return;
        case "sceneTrace":
          logScene("webview: " + String(msg.msg ?? ""));
          return;
        case "sceneError": {
          const text = String(msg.msg ?? "");
          logScene("webview error: " + text);
          void vscode.window.showWarningMessage("nautilus: " + text);
          return;
        }
        case "toggleLive":
          void vscode.commands.executeCommand("nautilus.liveValues.toggle");
          return;
        case "reopenAsText":
          reopenAsText(document.uri);
          return;
        case "setSnapToGrid":
          setSnapConfig(!!msg.value);
          return;
        case "sceneOp": {
          const op = msg.op;
          if (!op) return;
          this.opQueue = this.opQueue
            .then(async () => {
              logScene("op: " + JSON.stringify(op));
              const res = applySceneOp(document.getText(), op);
              if ("error" in res) {
                logScene("  -> refused: " + res.error);
                void vscode.window.showWarningMessage("nautilus: " + res.error);
                return;
              }
              if (res.text === document.getText()) {
                logScene("  -> no change");
                return;
              }
              const edit = new vscode.WorkspaceEdit();
              edit.replace(document.uri, new vscode.Range(0, 0, document.lineCount, 0), res.text);
              const ok = await vscode.workspace.applyEdit(edit);
              logScene(`  -> applied=${ok}`);
              // Don't wait out the change-event debounce: the webview holds
              // an optimistic position until the doc confirms — answer now.
              if (ok) postDoc();
            })
            .catch((err) => logScene("  -> exception: " + String(err)));
          return;
        }
      }
    });

    panel.onDidDispose(() => {
      if (debounce) clearTimeout(debounce);
      clearInterval(tagTimer);
      changeSub.dispose();
      configSub.dispose();
      messageSub.dispose();
    });

    postDoc();
    postConfig();
    void postTags();
  }
}
