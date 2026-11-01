// Component preview: see a component while you edit it. A `{Name}.svelte`
// open in VS Code's own text editor gets a pane beside it that renders the
// component for real — compiled with the project's own svelte + esbuild
// (userComponentBuild.ts, the same harness the mimic editor's user islands
// use) from the DIRTY buffer, so the pane tracks typing, not just saves —
// with sample props from the `preview` key of its `{Name}.component.json`
// sidecar, editable from the pane's props strip, and the sidecar's `ports`
// drawn over the render and edited there too (one pane per component: its
// render, its sample props, its connection points). It is THE editor for a
// sidecar: a `{Name}.component.json` opens as plain JSON, and its preview
// button lands here on the component it describes — a built-in (Tank,
// Pump, …) rendered from the kit's own registry, since there is no source
// to compile, with the kit's default ports shown until the sidecar
// overrides them. VS Code supplies the rest
// (the editor, the split, undo, the Svelte language server); this file is
// only the pane and its build loop. Like the FBD preview, the pane is a
// singleton that follows the active editor from one component to the next.
//
// The preview's bundle is its own file (component-preview.js), never the
// shared user-components.js the mimic panels load: a
// half-typed component must not break every open mimic, and the shared
// manager (userComponents.ts) rebuilds from DISK on save, which is the
// right moment for those panels. A failed compile keeps the last good
// bundle where it is and shows the error over it, so the pane never blanks
// mid-keystroke. The webview swaps bundles by appending a fresh <script>
// (PreviewApp.svelte) rather than reloading the whole page, so the strip
// and the stage stay put between compiles.
//
// Trust: gated on vscode.workspace.isTrusted exactly as userComponents.ts
// is — this compiles and RUNS the project's own code.

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import { webviewOptions } from "./fbdPreview";
import { buildUserComponentsBundle, type ComponentDiagnostic } from "./userComponentBuild";
import {
  componentNameFromFilename,
  componentNameFromSveltePath,
  parseComponentEntry,
  validatePortList,
  validatePreviewEntry,
} from "./mimicComponentIndex";
import {
  COMPONENT_GLOB,
  ComponentIndex,
  locateComponentSource,
  readText,
  sidecarLocation,
  writeComponentEntryEdit,
  writeComponentPortsEdit,
} from "./mimicComponents";
import type { UserComponentManager } from "./userComponents";
import { previewSources, previewTargetForPath } from "./componentPreviewLogic";
import { BUILTIN_COMPONENT_NAMES, BUILTIN_COMPONENT_PORTS } from "./builtinComponents";

const BUILD_DEBOUNCE_MS = 300;

let previewLog: vscode.OutputChannel | undefined;
/** The "nautilus preview" output channel: what was compiled, how long it
 * took, what the pane reported back — the first place to look when a
 * component shows nothing. */
function logPreview(msg: string): void {
  previewLog ??= vscode.window.createOutputChannel("nautilus preview");
  previewLog.appendLine(`[${new Date().toISOString()}] ${msg}`);
}
const DOC_DEBOUNCE_MS = 150;
const BUNDLE_FILE = "component-preview.js";

/** What the pane shows. A custom component's `uri` is its .svelte (compiled
 * from the buffer); a built-in's is only an anchor — the sidecar if one
 * exists, else wherever a new one should land — since the kit renders it. */
type Target = { uri: vscode.Uri; name: string; builtin: boolean };

/** The argument "Edit Component Ports…" passes for a built-in. */
export type PreviewArg = { name: string; uri: vscode.Uri };

function previewHtml(webview: vscode.Webview, extensionUri: vscode.Uri): string {
  // The mimic editor's bundle, mounted in its third mode (see
  // webview-ui/src/mimic/main.ts). No user-components tag here: the
  // preview bundle arrives by message (previewBundle) and is appended by
  // the app itself, so a rebuild never reloads the page. The page's CSP
  // nonce rides on <body data-nonce> for that: a script element the app
  // creates must carry it or the CSP blocks the load, and the nonce can't
  // be read back off the loader tag (the browser hides it).
  const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "mimic-editor.js"));
  const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "mimic-editor.css"));
  const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}'; img-src ${webview.cspSource} data:;">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="stylesheet" href="${styleUri}">
<title>Component Preview</title>
</head>
<body data-mimic-mode="preview" data-nonce="${nonce}">
<div id="app"></div>
<script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
}

export class ComponentPreview implements vscode.Disposable {
  private panel?: vscode.WebviewPanel;
  private target?: Target;
  private buildDebounce?: NodeJS.Timeout;
  private docDebounce?: NodeJS.Timeout;
  private disposables: vscode.Disposable[] = [];
  private building = false;
  private rebuildQueued = false;
  /** Cache-busting generation of the last GOOD bundle; 0 = none yet. */
  private version = 0;
  private diagnostics: ComponentDiagnostic[] = [];
  private readonly resourceRoot: vscode.Uri;
  private readonly bundleFsPath: string;

  constructor(
    private readonly context: vscode.ExtensionContext,
    userComponents: UserComponentManager,
    private readonly index: ComponentIndex
  ) {
    // Same workspace-storage directory as the shared user-components
    // bundle, so one localResourceRoots entry covers both.
    this.resourceRoot = userComponents.resourceRoot;
    this.bundleFsPath = vscode.Uri.joinPath(this.resourceRoot, BUNDLE_FILE).fsPath;

    const sidecars = vscode.workspace.createFileSystemWatcher(COMPONENT_GLOB);
    const sidecarChanged = (uri: vscode.Uri) => {
      if (this.isTargetSidecar(uri)) this.scheduleDoc();
    };
    this.disposables.push(
      sidecars,
      sidecars.onDidCreate(sidecarChanged),
      sidecars.onDidChange(sidecarChanged),
      sidecars.onDidDelete(sidecarChanged),
      vscode.workspace.onDidChangeTextDocument((e) => {
        if (!this.panel || !this.target) return;
        // Any Svelte buffer: the previewed one, or something it imports.
        if (e.document.uri.path.endsWith(".svelte")) this.scheduleBuild();
        else sidecarChanged(e.document.uri);
      }),
      vscode.window.onDidChangeActiveTextEditor((ed) => {
        // Follow the active component, like the markdown preview — its
        // .svelte, or its sidecar.
        if (!this.panel || !ed) return;
        void this.follow(ed.document.uri);
      }),
      vscode.workspace.onDidGrantWorkspaceTrust(() => {
        if (this.panel) this.scheduleBuild();
      })
    );
  }

  /** Open (or reveal) the preview: for a built-in named by the caller
   * (Edit Component Ports…), else for the active file — a component's
   * .svelte, or a `{Name}.component.json` (a built-in's straight from the
   * kit; a custom one's from its .svelte, which opens beside). */
  async preview(arg?: PreviewArg): Promise<void> {
    if (arg && BUILTIN_COMPONENT_NAMES.includes(arg.name)) {
      this.ensurePanel();
      this.retarget({ uri: arg.uri, name: arg.name, builtin: true });
      return;
    }
    const doc = vscode.window.activeTextEditor?.document;
    const target = doc ? await this.targetFor(doc.uri, true) : null;
    if (!target) {
      if (this.panel && this.target) {
        this.panel.reveal(undefined, true);
        return;
      }
      void vscode.window.showErrorMessage(
        "nautilus: open a component's .svelte or .component.json first (a SvelteKit +page.svelte or route file isn't a component)"
      );
      return;
    }
    this.ensurePanel();
    this.retarget(target);
  }

  /** The target `uri` previews, or null when it previews nothing. A custom
   * component's sidecar resolves to its .svelte; with `open`, that source
   * is also shown (the pane sits beside the text it tracks). */
  private async targetFor(uri: vscode.Uri, open: boolean): Promise<Target | null> {
    const what = previewTargetForPath(uri.fsPath, BUILTIN_COMPONENT_NAMES, componentNameFromSveltePath, componentNameFromFilename);
    if (!what) return null;
    if (what.kind === "svelte") return { uri, name: what.name, builtin: false };
    if (what.kind === "builtin") return { uri, name: what.name, builtin: true };
    const source = await locateComponentSource(what.name);
    if (!source) {
      if (open) {
        void vscode.window.showWarningMessage(
          `nautilus: no ${what.name}.svelte in the workspace to render — edit the JSON by hand, or open the component's source`
        );
      }
      return null;
    }
    if (open) await vscode.window.showTextDocument(source, { preserveFocus: true, viewColumn: vscode.ViewColumn.Active });
    return { uri: source, name: what.name, builtin: false };
  }

  private async follow(uri: vscode.Uri): Promise<void> {
    const target = await this.targetFor(uri, false);
    if (target && this.panel) this.retarget(target);
  }

  private retarget(next: Target): void {
    if (!this.panel) return;
    if (this.target?.uri.toString() === next.uri.toString() && this.target.name === next.name) {
      this.panel.reveal(undefined, true);
      return;
    }
    this.target = next;
    this.panel.title = `Preview: ${next.name}`;
    logPreview(`target ${next.name} (${next.builtin ? "built-in; anchor " : ""}${next.uri.fsPath})`);
    this.scheduleDoc();
    if (next.builtin) {
      // Nothing to compile: the kit's registry renders it. Clear what the
      // last custom component left.
      if (this.buildDebounce) clearTimeout(this.buildDebounce);
      this.diagnostics = [];
      this.postDiagnostics();
    } else {
      this.scheduleBuild();
    }
  }

  private isTargetSidecar(uri: vscode.Uri): boolean {
    if (!this.target) return false;
    return path.basename(uri.path) === `${this.target.name}.component.json`;
  }

  private ensurePanel(): void {
    if (this.panel) {
      this.panel.reveal(undefined, true);
      return;
    }
    this.panel = vscode.window.createWebviewPanel(
      "nautilusComponentPreview",
      "Component Preview",
      { viewColumn: vscode.ViewColumn.Beside, preserveFocus: true },
      { ...webviewOptions(this.context.extensionUri, [this.resourceRoot]), retainContextWhenHidden: true }
    );
    this.panel.webview.html = previewHtml(this.panel.webview, this.context.extensionUri);
    this.panel.onDidDispose(() => {
      this.panel = undefined;
      this.target = undefined;
      if (this.buildDebounce) clearTimeout(this.buildDebounce);
      if (this.docDebounce) clearTimeout(this.docDebounce);
    });
    this.panel.webview.onDidReceiveMessage((msg: { type?: string; props?: unknown; ports?: unknown; seq?: unknown; message?: unknown }) => {
      switch (msg?.type) {
        case "previewLog":
          logPreview(`pane: ${String(msg.message)}`);
          return;
        // Handshake: the one-shot posts below can beat the app's listener.
        case "previewReady":
          void this.postDoc();
          this.postBundle();
          this.postDiagnostics();
          return;
        case "previewSetProps":
          void this.writeProps(msg.props);
          return;
        case "previewSetPorts":
          void this.writePorts(msg.ports, typeof msg.seq === "number" ? msg.seq : undefined);
          return;
        case "openSidecar":
          void this.openSidecar();
          return;
        case "openSource":
          if (this.target && !this.target.builtin) void vscode.window.showTextDocument(this.target.uri, { preserveFocus: false });
          return;
      }
    });
  }

  // ── the document: name + sample props from the sidecar ──────────────────

  private scheduleDoc(): void {
    if (this.docDebounce) clearTimeout(this.docDebounce);
    this.docDebounce = setTimeout(() => void this.postDoc(), DOC_DEBOUNCE_MS);
  }

  /** `ack`: the seq of the pane's ports write this doc confirms (read after
   * that write), so the pane can tell it from an older re-read. */
  private async postDoc(ack?: number): Promise<void> {
    const target = this.target;
    if (!this.panel || !target) return;
    const sidecar = await sidecarLocation(this.index, target.uri, target.name);
    const text = await readText(sidecar);
    const entry = parseComponentEntry(text);
    const props = validatePreviewEntry(entry.preview) ? (entry.preview.props ?? {}) : {};
    // A built-in without a ports override shows the kit's defaults; the
    // first edit writes the whole list, defaults included, to the sidecar.
    const own = validatePortList(entry.ports) ? entry.ports : null;
    const inherited = own === null && target.builtin;
    const ports = own ?? (inherited ? (BUILTIN_COMPONENT_PORTS[target.name] ?? []) : []);
    if (this.target !== target) return; // moved on while reading
    void this.panel.webview.postMessage({
      type: "previewDoc",
      component: target.name,
      file: target.builtin ? target.name : path.basename(target.uri.path),
      builtin: target.builtin,
      sidecar: path.basename(sidecar.path),
      hasSidecar: text.trim() !== "",
      props,
      ports,
      portsInherited: inherited,
      ...(ack === undefined ? {} : { ack }),
    });
  }

  /** The props strip's edit: patch the sidecar's `preview.props` (creating
   * the sidecar next to the .svelte when there is none) as one
   * WorkspaceEdit — undoable, diffable, and exactly what the AI that wrote
   * the component would have written for it. */
  private async writeProps(props: unknown): Promise<void> {
    const target = this.target;
    if (!target || typeof props !== "object" || props === null || Array.isArray(props)) return;
    const map = props as Record<string, unknown>;
    const preview = Object.keys(map).length === 0 ? null : { props: map };
    const res = await writeComponentEntryEdit(this.index, target.uri, target.name, { preview }, "preview props");
    if (!res.ok && res.error) void vscode.window.showWarningMessage(`nautilus: ${res.error}`);
    void this.postDoc();
  }

  /** The ports overlay's edit: the whole list to the sidecar's `ports`
   * key, the same path the standalone *.component.json editor and the
   * mimic editor's ports mode take. Always answered with a previewDoc
   * carrying `seq` as its `ack` — written, refused or failed — since the
   * pane holds off the host's ports while an edit of its own is unconfirmed. */
  private async writePorts(ports: unknown, seq?: number): Promise<void> {
    const target = this.target;
    try {
      if (!target || !validatePortList(ports)) return;
      const res = await writeComponentPortsEdit(this.index, target.uri, target.name, ports);
      if (!res.ok && res.error) void vscode.window.showWarningMessage(`nautilus: ${res.error}`);
    } finally {
      void this.postDoc(seq);
    }
  }

  private async openSidecar(): Promise<void> {
    const target = this.target;
    if (!target) return;
    const sidecar = await sidecarLocation(this.index, target.uri, target.name);
    try {
      await vscode.workspace.fs.stat(sidecar);
    } catch {
      void vscode.window.showInformationMessage(
        `nautilus: ${target.name} has no ${target.name}.component.json yet — add a prop in the preview to create it`
      );
      return;
    }
    void vscode.window.showTextDocument(sidecar, { preserveFocus: false });
  }

  // ── the bundle: compile the buffers, swap on success ────────────────────

  private scheduleBuild(): void {
    if (this.buildDebounce) clearTimeout(this.buildDebounce);
    this.buildDebounce = setTimeout(() => void this.rebuild(), BUILD_DEBOUNCE_MS);
  }

  private async rebuild(): Promise<void> {
    const target = this.target;
    if (!this.panel || !target || target.builtin) return;
    if (!vscode.workspace.isTrusted) {
      this.diagnostics = [
        { component: "*", message: "the preview compiles and runs this project's own code — trust the workspace to turn it on" },
      ];
      this.postDiagnostics();
      return;
    }
    if (this.building) {
      this.rebuildQueued = true;
      return;
    }
    this.building = true;
    this.postDiagnostics();
    try {
      const sources = previewSources(
        vscode.workspace.textDocuments.map((d) => ({
          fsPath: d.uri.fsPath,
          isSvelte: d.uri.scheme === "file" && d.uri.path.endsWith(".svelte"),
          isDirty: d.isDirty,
          text: () => d.getText(),
        })),
        target.uri.fsPath
      );
      await fs.promises.mkdir(path.dirname(this.bundleFsPath), { recursive: true });
      // Build beside the live bundle and promote only a bundle that has
      // the component in it: a compile error leaves the last good render
      // on screen, under the error.
      const staging = this.bundleFsPath + ".next";
      const t0 = Date.now();
      const result = await buildUserComponentsBundle({ [target.name]: target.uri.fsPath }, staging, { sources });
      logPreview(
        `built ${target.name} in ${Date.now() - t0} ms: built=[${result.built.join(", ")}] diagnostics=${JSON.stringify(result.diagnostics)} sources=${Object.keys(sources).length}`
      );
      if (this.target !== target) return; // retargeted mid-build; the queued rebuild covers it
      if (result.built.includes(target.name)) {
        await fs.promises.rename(staging, this.bundleFsPath);
        this.version++;
        this.postBundle();
      }
      this.diagnostics = result.diagnostics;
    } catch (err) {
      logPreview(`build failed: ${String(err)}`);
      this.diagnostics = [{ component: "*", message: String(err) }];
    } finally {
      this.building = false;
      this.postDiagnostics();
      if (this.rebuildQueued) {
        this.rebuildQueued = false;
        void this.rebuild();
      }
    }
  }

  private postBundle(): void {
    if (!this.panel || this.version === 0) return;
    const raw = this.panel.webview.asWebviewUri(vscode.Uri.file(this.bundleFsPath)).toString();
    const src = raw + (raw.includes("?") ? "&" : "?") + "v=" + this.version;
    logPreview(`bundle v${this.version} -> ${src}`);
    void this.panel.webview.postMessage({ type: "previewBundle", src, version: this.version });
  }

  private postDiagnostics(): void {
    if (!this.panel) return;
    void this.panel.webview.postMessage({
      type: "previewDiagnostics",
      building: this.building,
      diagnostics: this.diagnostics,
    });
  }

  dispose(): void {
    if (this.buildDebounce) clearTimeout(this.buildDebounce);
    if (this.docDebounce) clearTimeout(this.docDebounce);
    this.panel?.dispose();
    for (const d of this.disposables) d.dispose();
  }
}

/** "nautilus: Open Component Preview" — the editor-title button on a
 * `.svelte` or a `.component.json`, its Command Palette entry, and the
 * target of "Edit Component Ports…" (which passes a PreviewArg for a
 * built-in). */
export function registerComponentPreview(
  context: vscode.ExtensionContext,
  userComponents: UserComponentManager,
  index: ComponentIndex
): vscode.Disposable {
  const preview = new ComponentPreview(context, userComponents, index);
  return vscode.Disposable.from(
    preview,
    vscode.commands.registerCommand("nautilus.component.preview", (arg?: PreviewArg) => preview.preview(arg))
  );
}
