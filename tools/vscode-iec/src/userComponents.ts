// User-authored Svelte components INSIDE the mimic editor webview
// ("self-contained islands compiled with the user's own dependencies"): a
// mimic referencing a non-built-in component (or a *.component.json sidecar
// for one) gets its `{Name}.svelte` found in the workspace, compiled with
// THAT PROJECT's own svelte + esbuild (userComponentBuild.ts does the actual
// compiling; this file is the vscode wiring around it — discovery via
// findFiles, a FileSystemWatcher, debounced rebuilds, and pushing the result
// to every open panel), and mounted for real in place of the placeholder
// chip (mimic/UserIsland.svelte, webview side).
//
// One instance is shared by every open mimic + component editor panel (same
// shape as ComponentIndex in mimicComponents.ts) so a panel that references
// a component nobody else does still gets it built, and every panel sees the
// same bundle + diagnostics.
//
// Trust: gated on vscode.workspace.isTrusted — this compiles and RUNS
// arbitrary project code (via esbuild + the project's own svelte/compiler),
// so an untrusted workspace never builds anything; granting trust later
// (onDidGrantWorkspaceTrust) builds whatever was already requested.

import * as vscode from "vscode";
import * as path from "path";
import { locateComponentSource } from "./mimicComponents";
import { buildUserComponentsBundle, customComponentNames, type ComponentDiagnostic } from "./userComponentBuild";
import { BUILTIN_COMPONENT_NAMES } from "./builtinComponents";

/** The kit built-ins the editor already renders for real; everything else is
 * a candidate for a user-authored island. Sourced from builtinComponents.ts
 * (itself a manually-synced mirror of webview-ui/src/mimic/builtinPorts.ts —
 * see that file's comment) so there's exactly one host-side list of names. */
export const BUILTIN_COMPONENTS = new Set(BUILTIN_COMPONENT_NAMES);

const DEBOUNCE_MS = 400;
const BUNDLE_FILE = "user-components.js";

export class UserComponentManager implements vscode.Disposable {
  private readonly storageDir: vscode.Uri;
  private readonly bundleFsPath: string;
  private readonly onDidChangeEmitter = new vscode.EventEmitter<void>();
  /** Fires after every rebuild attempt (success or failure) — panels reload
   * their HTML (new bundle, cache-busted) and re-post diagnostics. */
  readonly onDidChange = this.onDidChangeEmitter.event;

  private trusted: boolean;
  private diagnostics: ComponentDiagnostic[] = [];
  private version = 0;
  private everBuilt = false;

  private readonly byPanel = new Map<vscode.WebviewPanel, Set<string>>();
  private pendingNames = new Set<string>();
  private building = false;
  private rebuildQueued = false;
  private debounce?: NodeJS.Timeout;
  private fileWatcher?: vscode.Disposable;

  constructor(context: vscode.ExtensionContext) {
    // Workspace storage, not global: components are workspace-specific code,
    // and globalStorageUri is shared across every simultaneously-open VS
    // Code window for this extension — two windows on two different
    // projects would otherwise race on the same bundle file. Falls back to
    // global storage on the rare "no folder open" case (nothing to
    // discover then anyway).
    const base = context.storageUri ?? context.globalStorageUri;
    this.storageDir = vscode.Uri.joinPath(base, "user-components");
    this.bundleFsPath = vscode.Uri.joinPath(this.storageDir, BUNDLE_FILE).fsPath;
    this.trusted = vscode.workspace.isTrusted;
  }

  /** The bundle's webview-facing URI once at least one build has run, else
   * undefined (no script tag is emitted for a bundle that's never existed). */
  bundleUri(): vscode.Uri | undefined {
    return this.everBuilt ? vscode.Uri.file(this.bundleFsPath) : undefined;
  }

  /** Cache-busting counter for the bundle's <script src>, so reassigning
   * webview.html after a rebuild is guaranteed to reload it. */
  bundleVersion(): number {
    return this.version;
  }

  lastDiagnostics(): ComponentDiagnostic[] {
    return this.diagnostics;
  }

  /** localResourceRoots addition for webviewOptions — harmless to include
   * even before the directory exists on disk. */
  get resourceRoot(): vscode.Uri {
    return this.storageDir;
  }

  /** Called once per panel when it resolves — tracks the panel so its
   * contribution to `pendingNames` is dropped when it closes. */
  registerPanel(panel: vscode.WebviewPanel): void {
    this.byPanel.set(panel, new Set());
    panel.onDidDispose(() => {
      this.byPanel.delete(panel);
      this.recomputeAndSchedule();
    });
  }

  /** A panel's document references these component names (mimic equipment,
   * or the one component a *.component.json sidecar is for) — recomputes
   * the union across every open panel and (re)schedules a build if it grew. */
  request(panel: vscode.WebviewPanel, names: Iterable<string>): void {
    const set = new Set(customComponentNames(names, BUILTIN_COMPONENTS));
    this.byPanel.set(panel, set);
    this.recomputeAndSchedule();
  }

  /** Workspace trust granted after activation: build whatever was already
   * requested (buttons/panels don't need to re-announce). */
  activateTrust(): void {
    if (this.trusted) return;
    this.trusted = true;
    if (this.pendingNames.size) this.scheduleBuild();
  }

  private recomputeAndSchedule(): void {
    const union = new Set<string>();
    for (const s of this.byPanel.values()) for (const n of s) union.add(n);
    if (setsEqual(union, this.pendingNames)) return;
    this.pendingNames = union;
    this.scheduleBuild();
  }

  private scheduleBuild(): void {
    if (!this.trusted) return;
    if (this.debounce) clearTimeout(this.debounce);
    this.debounce = setTimeout(() => void this.rebuild(), DEBOUNCE_MS);
  }

  private async rebuild(): Promise<void> {
    if (this.building) {
      this.rebuildQueued = true;
      return;
    }
    this.building = true;
    try {
      const names = [...this.pendingNames];
      const located = new Map<string, vscode.Uri>();
      const missing: ComponentDiagnostic[] = [];
      for (const name of names) {
        const uri = await locateComponentSource(name);
        if (uri) located.set(name, uri);
        else missing.push({ component: name, message: `no ${name}.svelte found in this workspace` });
      }

      // buildUserComponentsBundle writes an (empty-stub) bundle even for
      // `{}` — window.__NX_USER_COMPONENTS__ is always defined once any
      // build has run, so a panel referencing only missing components still
      // gets a safe script tag.
      await vscode.workspace.fs.createDirectory(this.storageDir);
      const componentPaths: Record<string, string> = {};
      for (const [name, uri] of located) componentPaths[name] = uri.fsPath;
      const result = await buildUserComponentsBundle(componentPaths, this.bundleFsPath);

      this.diagnostics = [...missing, ...result.diagnostics];
      this.everBuilt = true;
      this.version++;
      this.setupWatcher(located);
    } catch (err) {
      // Belt and braces: buildUserComponentsBundle already swallows its own
      // failures, but discovery (findFiles) or fs errors land here — never
      // let a rebuild take a panel down.
      this.diagnostics = [{ component: "*", message: String(err) }];
    } finally {
      this.building = false;
      this.onDidChangeEmitter.fire();
      if (this.rebuildQueued) {
        this.rebuildQueued = false;
        void this.rebuild();
      }
    }
  }

  /** Rebuild whenever any discovered component's OWN .svelte file changes —
   * editing HeatExchanger.svelte live-updates the mimic editor,
   * the same way editing the .mimic.json text does. */
  private setupWatcher(located: Map<string, vscode.Uri>): void {
    this.fileWatcher?.dispose();
    if (located.size === 0) {
      this.fileWatcher = undefined;
      return;
    }
    const watchers = [...located.values()].map((uri) => {
      const pattern = new vscode.RelativePattern(vscode.Uri.joinPath(uri, ".."), path.basename(uri.fsPath));
      const w = vscode.workspace.createFileSystemWatcher(pattern);
      const fire = () => this.scheduleBuild();
      return vscode.Disposable.from(w, w.onDidChange(fire), w.onDidCreate(fire), w.onDidDelete(fire));
    });
    this.fileWatcher = vscode.Disposable.from(...watchers);
  }

  dispose(): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.fileWatcher?.dispose();
    this.onDidChangeEmitter.dispose();
  }
}

function setsEqual(a: Set<string>, b: Set<string>): boolean {
  if (a.size !== b.size) return false;
  for (const v of a) if (!b.has(v)) return false;
  return true;
}

/** Shared by mimicEditor.ts (and once the sidecar editor): the user-components
 * <script> tag, cache-busted so reassigning webview.html after a rebuild is
 * guaranteed to reload it — omitted entirely until a bundle has ever been
 * built (an untrusted workspace, or a doc with no custom components, never
 * gets one). */
export function userComponentScriptTag(
  webview: vscode.Webview,
  userComponents: UserComponentManager,
  nonce: string
): string {
  const uri = userComponents.bundleUri();
  if (!uri) return "";
  const raw = webview.asWebviewUri(uri).toString();
  const src = raw + (raw.includes("?") ? "&" : "?") + "v=" + userComponents.bundleVersion();
  return `\n<script nonce="${nonce}" src="${src}"></script>`;
}
