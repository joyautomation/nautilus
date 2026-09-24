// Mimic editor: "the FBD editor for HMIs". A *.mimic.json document opens as
// a graphical P&ID editor (drag equipment, route pipes, bind tags) while the
// text stays canonical — every gesture becomes an op (mimicOps.ts), applied
// as one WorkspaceEdit so text undo/redo covers the whole session, and
// editing the JSON side by side updates the canvas live.
//
// When a controller is reachable (nautilus.runtimeUrl) and live values are on
// (nautilus.liveValues.enabled — the same switch as the text editors' pills
// and the diagrams' live pill), the editor polls GET /api/state and renders
// the mimic LIVE while you edit — tag names feed binding autocomplete and
// bound components animate with the real process.

import * as vscode from "vscode";
import { webviewOptions } from "./fbdPreview";
import { applyMimicOp, parseMimic, type MimicOp } from "./mimicOps";
import { ComponentIndex, writeComponentPortsEdit } from "./mimicComponents";
import { paletteCustomComponents, type Port } from "./mimicComponentIndex";
import { BUILTIN_COMPONENTS, userComponentScriptTag, type UserComponentManager } from "./userComponents";

/** Mirror of webview-ui/src/mimic/mimicState.svelte.ts's ManifestOp. */
type ManifestOp = { type: "setComponentPorts"; component: string; ports: Port[] | null };

const DEBOUNCE_MS = 150;
const TAG_POLL_MS = 2000;

let mimicLog: vscode.OutputChannel | undefined;
function logMimic(msg: string): void {
  mimicLog ??= vscode.window.createOutputChannel("nautilus mimic");
  mimicLog.appendLine(`[${new Date().toISOString()}] ${msg}`);
}

function mimicHtml(webview: vscode.Webview, extensionUri: vscode.Uri, userComponents: UserComponentManager): string {
  const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "mimic-editor.js"));
  const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", "dist", "mimic-editor.css"));
  const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
  // The user-components bundle (userComponents.ts), when one has ever been
  // built, loads AFTER the editor bundle — deliberately: the mimic app's
  // own mount() call must not wait on it. It arrives a beat later, and
  // window.__NX_USER_COMPONENTS__'s "ready" event (UserIsland.svelte) is
  // what upgrades an already-rendered placeholder chip to the real island.
  const userScript = userComponentScriptTag(webview, userComponents, nonce);
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="stylesheet" href="${styleUri}">
<title>Mimic</title>
</head>
<body data-mimic-mode="mimic">
<div id="app"></div>
<script nonce="${nonce}" src="${scriptUri}"></script>${userScript}
</body>
</html>`;
}

/** Read the persisted snap-to-grid preference (`nautilus.mimic.snapToGrid`,
 * default on — matches the pre-toggle behavior). */
function snapToGridConfig(): boolean {
  return vscode.workspace.getConfiguration("nautilus").get<boolean>("mimic.snapToGrid", true);
}

/** Flip the setting and persist it. Same scope convention as the live-values
 * toggle (liveValues.ts): a scaffolded project's workspace
 * .vscode/settings.json would override a Global write, so target Workspace
 * whenever a folder is open, Global otherwise — either way the preference
 * survives an editor/window reload. */
function setSnapToGridConfig(value: boolean): void {
  const target = vscode.workspace.workspaceFolders?.length
    ? vscode.ConfigurationTarget.Workspace
    : vscode.ConfigurationTarget.Global;
  void vscode.workspace.getConfiguration("nautilus").update("mimic.snapToGrid", value, target);
}

/** The shared live-values switch (liveValues.ts owns it; the toolbar pill
 * flips it through the same `nautilus.liveValues.toggle` command). */
function liveEnabled(): boolean {
  return vscode.workspace.getConfiguration("nautilus").get<boolean>("liveValues.enabled", true);
}

/** "Reopen as Text Editor" from an editor whose document doesn't parse:
 * `default` is VS Code's built-in text editor for any resource. */
export function reopenAsText(uri: vscode.Uri): void {
  void vscode.commands.executeCommand("vscode.openWith", uri, "default");
}

/** Fetch the controller's tag snapshot, or null when unreachable. */
async function fetchTags(): Promise<Record<string, unknown> | null> {
  const url = vscode.workspace.getConfiguration("nautilus").get<string>("runtimeUrl", "http://localhost:8080");
  try {
    const resp = await fetch(`${url}/api/state`, { signal: AbortSignal.timeout(1500) });
    if (!resp.ok) return null;
    const state = (await resp.json()) as { tags?: Record<string, unknown> };
    return state.tags ?? {};
  } catch {
    return null;
  }
}

/** "Open With → Mimic Editor" (the DEFAULT for *.mimic.json — a mimic is
 * spatial-first; the JSON is a right-click "Open With → Text Editor" away). */
export class MimicEditorProvider implements vscode.CustomTextEditorProvider {
  static readonly viewType = "nautilus.mimicEditor";

  /** Ops apply strictly in order: each must read the text AFTER the
   * previous edit landed (same rule as the FBD/ladder queues). */
  private opQueue: Promise<void> = Promise.resolve();
  /** Ports edits target a sidecar file, not the document, so they get
   * their own ordering queue rather than sharing opQueue's. */
  private portsQueue: Promise<void> = Promise.resolve();

  /** One index for the whole workspace, shared by every open mimic panel (and
   * the "Edit Component Ports…" command — see extension.ts's
   * `mimicProvider.componentIndex` — hence public) — a sidecar edited by one
   * panel, by hand, or by git, updates them all. Built once and kept live by
   * a single FileSystemWatcher for the life of the extension, not per panel. */
  readonly componentIndex = new ComponentIndex();
  private readonly portsReady: Promise<void>;
  private readonly portsWatcher: vscode.Disposable;
  private readonly panels = new Set<vscode.WebviewPanel>();
  /** Each open panel's doc's referenced equipment component names — the
   * other half (besides sidecars) of the palette's "custom components"
   * list (see paletteCustomComponents). Updated every postDoc(); read back
   * in broadcastPorts() so a sidecar-index change re-broadcasts the right
   * list for EACH panel's own doc, not just whichever panel last edited. */
  private readonly panelDocNames = new Map<vscode.WebviewPanel, string[]>();

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly userComponents: UserComponentManager
  ) {
    this.portsReady = this.componentIndex.refresh();
    this.portsWatcher = this.componentIndex.watch(() => this.broadcastPorts());
  }

  /** The `mimicManifest` payload for one panel: the shared sidecar index
   * plus THAT panel's own custom-components palette list (its doc's
   * equipment names union the sidecar index, minus built-ins). */
  private manifestMessage(panel: vscode.WebviewPanel): { type: "mimicManifest"; components: unknown; customComponents: string[] } {
    return {
      type: "mimicManifest",
      components: this.componentIndex.manifest,
      customComponents: paletteCustomComponents(
        Object.keys(this.componentIndex.manifest),
        this.panelDocNames.get(panel) ?? [],
        BUILTIN_COMPONENTS
      ),
    };
  }

  /** Push the current ports index (+ each panel's custom-components palette
   * list) to every open mimic panel — used after a write from any one of
   * them, and after an outside change the watcher picks up (hand-editing a
   * sidecar, git, another panel). */
  private broadcastPorts(): void {
    for (const panel of this.panels) {
      void panel.webview.postMessage(this.manifestMessage(panel));
    }
  }

  register(): vscode.Disposable {
    return vscode.Disposable.from(
      vscode.window.registerCustomEditorProvider(MimicEditorProvider.viewType, this, {
        webviewOptions: { retainContextWhenHidden: true },
        supportsMultipleEditorsPerDocument: true,
      }),
      this.portsWatcher
    );
  }

  async resolveCustomTextEditor(document: vscode.TextDocument, panel: vscode.WebviewPanel): Promise<void> {
    panel.webview.options = webviewOptions(this.context.extensionUri, [this.userComponents.resourceRoot]);
    panel.webview.html = mimicHtml(panel.webview, this.context.extensionUri, this.userComponents);
    this.userComponents.registerPanel(panel);

    const postUserComponentDiagnostics = () =>
      void panel.webview.postMessage({
        type: "userComponentDiagnostics",
        diagnostics: this.userComponents.lastDiagnostics(),
      });

    const postDoc = () => {
      try {
        const doc = parseMimic(document.getText());
        void panel.webview.postMessage({
          type: "mimicDoc",
          doc,
          title: document.uri.path.split("/").pop() ?? "mimic",
        });
        const names = (doc.equipment ?? []).map((e) => e.component);
        // Every equipment instance's component is a candidate user island —
        // registry built-ins get filtered out inside request() itself.
        this.userComponents.request(panel, names);
        // The palette's custom-components list depends on this doc's own
        // equipment (besides the shared sidecar index) — remember it here
        // and re-broadcast now, since the doc just changed.
        this.panelDocNames.set(panel, names);
        postPorts();
      } catch (err) {
        void panel.webview.postMessage({
          type: "mimicError",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    };

    // A rebuilt user-components bundle needs a fresh <script src> to take —
    // reassigning webview.html reloads the whole page; the ready-handshake
    // below re-answers with the current doc/ports/tags once it remounts.
    const userComponentsSub = this.userComponents.onDidChange(() => {
      panel.webview.html = mimicHtml(panel.webview, this.context.extensionUri, this.userComponents);
      postUserComponentDiagnostics();
    });

    let debounce: NodeJS.Timeout | undefined;
    const changeSub = vscode.workspace.onDidChangeTextDocument((e) => {
      if (e.document.uri.toString() !== document.uri.toString()) return;
      if (debounce) clearTimeout(debounce);
      debounce = setTimeout(postDoc, DEBOUNCE_MS);
    });

    // Snap-to-grid preference: a config setting rather than webview-only
    // state so it survives editor reloads (and is discoverable/settable
    // from Settings directly, not just the toolbar toggle). Re-broadcast on
    // an external change (Settings UI, another panel's toggle, git) same as
    // the ports index does.
    const postConfig = () => void panel.webview.postMessage({ type: "mimicConfig", snapToGrid: snapToGridConfig() });
    const configSub = vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("nautilus.mimic.snapToGrid")) postConfig();
      if (e.affectsConfiguration("nautilus.liveValues.enabled") || e.affectsConfiguration("nautilus.runtimeUrl")) {
        void postTags();
      }
    });

    // *.component.json sidecars: the project-wide index (mimicComponents.ts)
    // is shared across every open panel — this one just registers to
    // receive its broadcasts and asks for the current snapshot once ready.
    this.panels.add(panel);
    const postPorts = () => void panel.webview.postMessage(this.manifestMessage(panel));

    const messageSub = panel.webview.onDidReceiveMessage(
      (msg: { type?: string; op?: MimicOp; msg?: unknown } | { type: "manifestOp"; op: ManifestOp }) => {
        // The webview announces when its listener is up; answer with the
        // current state. The unsolicited postDoc below can beat the app's
        // mount and be dropped — this handshake is the delivery guarantee.
        if (msg?.type === "mimicReady") {
          postDoc();
          void this.portsReady.then(postPorts);
          void postTags();
          postUserComponentDiagnostics();
          postConfig();
          return;
        }
        if (msg?.type === "mimicTrace") {
          logMimic("webview: " + String((msg as { msg?: unknown }).msg ?? ""));
          return;
        }
        if (msg?.type === "toggleLive") {
          void vscode.commands.executeCommand("nautilus.liveValues.toggle");
          return;
        }
        if (msg?.type === "reopenAsText") {
          reopenAsText(document.uri);
          return;
        }
        if (msg?.type === "setSnapToGrid") {
          setSnapToGridConfig(!!(msg as { value?: boolean }).value);
          return;
        }
        if (msg?.type === "manifestOp") {
          const op = msg.op;
          if (!op || op.type !== "setComponentPorts" || !op.component) return;
          this.portsQueue = this.portsQueue
            .then(async () => {
              logMimic("manifestOp: " + JSON.stringify(op));
              const res = await writeComponentPortsEdit(this.componentIndex, document.uri, op.component, op.ports ?? null);
              logMimic(`  -> applied=${res.ok}${res.error ? " (" + res.error + ")" : ""}`);
              if (res.error) void vscode.window.showWarningMessage("nautilus: " + res.error);
              // Re-broadcast either way: on a refusal the webview's
              // optimistic ports snap back to what the sidecar really says.
              this.broadcastPorts();
            })
            .catch((err) => logMimic("  -> exception: " + String(err)));
          return;
        }
        if (msg?.type !== "mimicOp" || !("op" in msg) || !msg.op) return;
        this.opQueue = this.opQueue
          .then(async () => {
            logMimic("op: " + JSON.stringify(msg.op));
            const res = applyMimicOp(document.getText(), msg.op!);
            if ("error" in res) {
              logMimic("  -> refused: " + res.error);
              void vscode.window.showWarningMessage("nautilus: " + res.error);
              return;
            }
            if (res.text === document.getText()) {
              logMimic("  -> no change");
              return;
            }
            const edit = new vscode.WorkspaceEdit();
            edit.replace(document.uri, new vscode.Range(0, 0, document.lineCount, 0), res.text);
            const ok = await vscode.workspace.applyEdit(edit);
            logMimic(`  -> applied=${ok}`);
            // Don't wait out the change-event debounce: the webview holds an
            // optimistic position until the doc confirms — answer now.
            if (ok) postDoc();
          })
          .catch((err) => logMimic("  -> exception: " + String(err)));
      }
    );

    // Live tags: poll while the panel is visible AND live values are on;
    // tags null = controller offline (the webview shows the pill grey and
    // keeps component defaults). With live values off nothing is fetched at
    // all — the pill reads "live off" and clicking it turns them back on.
    const postTags = async () => {
      const enabled = liveEnabled();
      const tags = enabled ? await fetchTags() : null;
      void panel.webview.postMessage({ type: "mimicTags", tags, enabled });
    };
    const tagTimer = setInterval(() => {
      if (panel.visible && liveEnabled()) void postTags();
    }, TAG_POLL_MS);
    void postTags();

    panel.onDidDispose(() => {
      if (debounce) clearTimeout(debounce);
      clearInterval(tagTimer);
      changeSub.dispose();
      configSub.dispose();
      messageSub.dispose();
      userComponentsSub.dispose();
      this.panels.delete(panel);
      this.panelDocNames.delete(panel);
    });

    postDoc();
    void this.portsReady.then(postPorts);
    postConfig();
  }
}
