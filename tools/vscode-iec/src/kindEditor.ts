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
import { applyKindOp, parseKind, type KindDoc, type KindOp } from "./kindOps";
import { kindNameFromFilename, resolveKindModel } from "./kindEditorLogic";
import { componentNameFor, ejectSource, repointScene } from "./kindEject";
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

    const messageSub = panel.webview.onDidReceiveMessage((msg: { type?: string; op?: KindOp; msg?: unknown; bounds?: unknown; picked?: unknown; parts?: unknown }) => {
      switch (msg?.type) {
        case "kindEject":
          void this.eject(document, msg.bounds as { size: [number, number, number]; center: [number, number, number] } | null | undefined);
          return;
        case "kindAskAi":
          void this.askAi(document, typeof msg.picked === "string" ? msg.picked : null, Array.isArray(msg.parts) ? (msg.parts as string[]) : []);
          return;
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

  /** Eject to component (design §3g): write `{Name}.svelte` beside the
   * kind file — the model and the drives as code — and offer to point the
   * scenes that name this file at it. The kind file stays; the scene
   * entry is what changes, so a text undo brings the kind back. */
  private async eject(document: vscode.TextDocument, measured: { size: [number, number, number]; center: [number, number, number] } | null | undefined): Promise<void> {
    let kind: KindDoc;
    try {
      kind = parseKind(document.getText());
    } catch (err) {
      void vscode.window.showWarningMessage("nautilus: the kind does not parse: " + (err instanceof Error ? err.message : String(err)));
      return;
    }
    if (!kind.model) {
      void vscode.window.showWarningMessage("nautilus: a kind with no model has nothing to eject — a component kind is written from scratch (design §3d).");
      return;
    }
    const name = kindNameFromFilename(document.uri.fsPath);
    const kindDir = path.dirname(document.uri.fsPath);
    const target = vscode.Uri.file(path.join(kindDir, componentNameFor(name) + ".svelte"));
    if (fs.existsSync(target.fsPath)) {
      const pick = await vscode.window.showWarningMessage(`${path.basename(target.fsPath)} already exists beside ${path.basename(document.uri.fsPath)}.`, { modal: true }, "Overwrite");
      if (pick !== "Overwrite") return;
    }
    const source = ejectSource({ name, file: path.basename(document.uri.fsPath), kind, measured: measured ?? undefined, date: new Date().toISOString().slice(0, 10) });
    await vscode.workspace.fs.writeFile(target, Buffer.from(source, "utf8"));
    logKind(`ejected ${document.uri.fsPath} -> ${target.fsPath}`);
    // The scenes that name this kind file, offered a re-point.
    const scenes = await vscode.workspace.findFiles("**/*.scene.json", "**/{node_modules,.svelte-kit,build}/**", 200);
    const edits: { uri: vscode.Uri; text: string }[] = [];
    for (const uri of scenes) {
      try {
        const text = Buffer.from(await vscode.workspace.fs.readFile(uri)).toString("utf8");
        const next = repointScene(text, path.dirname(uri.fsPath), document.uri.fsPath, target.fsPath, kind);
        if (next !== undefined && next !== text) edits.push({ uri, text: next });
      } catch (err) {
        logKind(`eject: skipped ${uri.fsPath}: ${String(err)}`);
      }
    }
    if (edits.length) {
      const list = edits.map((e) => vscode.workspace.asRelativePath(e.uri)).join(", ");
      const pick = await vscode.window.showInformationMessage(
        `Point ${edits.length === 1 ? "the scene" : `${edits.length} scenes`} that name ${path.basename(document.uri.fsPath)} at the component? (${list}) The kind file stays.`,
        { modal: true },
        "Point the scene at it"
      );
      if (pick) {
        const edit = new vscode.WorkspaceEdit();
        for (const e of edits) {
          const doc = await vscode.workspace.openTextDocument(e.uri);
          edit.replace(e.uri, new vscode.Range(0, 0, doc.lineCount, 0), e.text);
        }
        await vscode.workspace.applyEdit(edit);
        for (const e of edits) await (await vscode.workspace.openTextDocument(e.uri)).save();
      }
    }
    await vscode.window.showTextDocument(target, { viewColumn: vscode.ViewColumn.Beside, preview: false });
    void vscode.window.showInformationMessage(`nautilus: ${path.basename(target.fsPath)} written — the model and the drives as code, yours to script.`);
  }

  /** The AI on-ramp: the open-ended work (a model from a picture, part
   * names, a variant) is the AI's; this picks a prompt about THIS file and
   * opens the chat with it, or copies it when no chat command is there. */
  private async askAi(document: vscode.TextDocument, picked: string | null, parts: string[]): Promise<void> {
    const rel = vscode.workspace.asRelativePath(document.uri);
    const name = kindNameFromFilename(document.uri.fsPath);
    let kind: Partial<KindDoc> = {};
    try {
      kind = parseKind(document.getText());
    } catch {
      // the prompt still names the file
    }
    const model = kind.model ?? "models/" + name + ".glb";
    const partList = parts.length ? parts.join(", ") : "(the model has not loaded)";
    const items: (vscode.QuickPickItem & { prompt: string })[] = [
      {
        label: "$(device-camera) A model from a picture",
        description: "make or remake the glTF",
        prompt: `Make a glTF model for the 3D kind "${name}" (${rel}), from the picture I attach. Export it as ${model} (metres, Y up, origin at the base, one mesh per part someone would animate — motor, impeller, lamp, flanges — named in PascalCase). Then update ${rel}: model, bounds if needed, and one drive per part that should react to a member (tint on a run bit, spin on a speed, emissive on a fault). Check it with \`naut check\`.`,
      },
      {
        label: "$(symbol-string) Name the parts",
        description: "sensible PascalCase names in the model",
        prompt: `In ${model} (the model of the 3D kind ${rel}) the parts are named: ${partList}. Rename them to short PascalCase names that say what each part is (Motor, Volute, Impeller, Lamp, FlangeIn, …), one mesh per thing a drive would target, and update the mesh names in ${rel}'s drives and ports to match.`,
      },
      {
        label: "$(symbol-event) Drives for the members",
        description: `wire ${kind.type ?? "the UDT"}'s members to parts`,
        prompt: `Add drives to ${rel} for the members of the ${kind.type ?? "UDT"} it binds${kind.members?.length ? ` (${kind.members.join(", ")})` : ""}: which part should tint, spin, turn, scale, glow, blink, pulse or hide on which member, in the kind file's drive vocabulary (spin, turn, scale, tint, emissive, visible, pulse; a tint or emissive may be a ramp of [value, colour]). The parts are: ${partList}.${picked ? ` Start with ${picked}.` : ""} Keep every change one drive per line.`,
      },
      {
        label: "$(versions) A variant of this kind",
        description: "the same model, different behaviour",
        prompt: `Make a variant of the 3D kind ${rel} as a second kind file beside it, sharing ${model}: say what differs (for a float switch: active-low — the "up" and "down" visible drives swap), and name the new file after the variant.`,
      },
      {
        label: "$(comment) Something else about this kind",
        description: "a prompt that names the file, to finish yourself",
        prompt: `About the 3D kind ${rel} (model ${model}; parts: ${partList}${picked ? `; the part I have picked is ${picked}` : ""}): `,
      },
    ];
    const pick = await vscode.window.showQuickPick(items, { placeHolder: "What should the AI do with this kind? (the tweaks stay yours: drives, ports, colours — in the panel)" });
    if (!pick) return;
    const commands = new Set(await vscode.commands.getCommands(true));
    if (commands.has("workbench.action.chat.open")) {
      try {
        await vscode.commands.executeCommand("workbench.action.chat.open", { query: pick.prompt, isPartialQuery: true });
        return;
      } catch (err) {
        logKind("chat.open failed: " + String(err));
      }
    }
    await vscode.env.clipboard.writeText(pick.prompt);
    if (commands.has("claude-vscode.sidebar.open")) {
      void vscode.window.showInformationMessage("nautilus: the prompt is on the clipboard — paste it into Claude Code.");
      await vscode.commands.executeCommand("claude-vscode.sidebar.open");
    } else {
      void vscode.window.showInformationMessage("nautilus: the prompt is on the clipboard — paste it into your AI chat (Copilot Chat, Claude Code, …).");
    }
  }
}
