// The web build's userComponents.ts: the mimic editor's custom components,
// compiled in the browser. It does what ../userComponents.ts does on a
// desktop (collect the components the open mimics use, build them into
// one script, rebuild when their files change), with web/
// componentCompiler.ts in place of the project's own svelte and esbuild.
//
// The compiler (Svelte's, and Sucrase: out/web/component-compiler.js,
// about 1 MB) loads the first time a mimic uses a custom component. The
// built script lives in memory under the nautilus-components: scheme,
// which the mimic webview loads after media/dist/user-runtime.js (Svelte
// and the HMI kit, window.__NX_RT__).
// Same exports as ../userComponents.ts, so mimicEditor.ts is unchanged.
import * as vscode from "vscode";
import { BUILTIN_COMPONENT_NAMES } from "../builtinComponents";
import { locateComponentSource } from "../mimicComponents";
import { customComponentNames, describeError, type ComponentDiagnostic } from "../userComponentEntry";
import type { compileComponents } from "./componentCompiler";

export const BUILTIN_COMPONENTS = new Set(BUILTIN_COMPONENT_NAMES);

const SCHEME = "nautilus-components";
const DEBOUNCE_MS = 400;

/** The built scripts, by path: the webview reads them through this. */
class BuiltScripts implements vscode.FileSystemProvider {
  readonly files = new Map<string, Uint8Array>();
  private readonly changed = new vscode.EventEmitter<vscode.FileChangeEvent[]>();
  readonly onDidChangeFile = this.changed.event;
  watch(): vscode.Disposable {
    return new vscode.Disposable(() => undefined);
  }
  stat(uri: vscode.Uri): vscode.FileStat {
    if (uri.path === "/") return { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
    const data = this.files.get(uri.path);
    if (!data) throw vscode.FileSystemError.FileNotFound(uri);
    return { type: vscode.FileType.File, ctime: 0, mtime: 0, size: data.length };
  }
  readDirectory(): [string, vscode.FileType][] {
    return [...this.files.keys()].map((p) => [p.slice(1), vscode.FileType.File]);
  }
  readFile(uri: vscode.Uri): Uint8Array {
    const data = this.files.get(uri.path);
    if (!data) throw vscode.FileSystemError.FileNotFound(uri);
    return data;
  }
  createDirectory(): void {
    throw vscode.FileSystemError.NoPermissions("read-only");
  }
  writeFile(): void {
    throw vscode.FileSystemError.NoPermissions("read-only");
  }
  delete(): void {
    throw vscode.FileSystemError.NoPermissions("read-only");
  }
  rename(): void {
    throw vscode.FileSystemError.NoPermissions("read-only");
  }
}

type Compile = typeof compileComponents;

/** The compiler chunk, evaluated once on first use. */
let compiler: Promise<Compile> | undefined;
function loadCompiler(extensionUri: vscode.Uri): Promise<Compile> {
  compiler ??= (async () => {
    const bytes = await vscode.workspace.fs.readFile(vscode.Uri.joinPath(extensionUri, "out", "web", "component-compiler.js"));
    const module: { exports: { compileComponents?: Compile } } = { exports: {} };
    const noRequire = (name: string) => {
      throw new Error(`component-compiler.js asked for ${name}`);
    };
    new Function("module", "exports", "require", new TextDecoder().decode(bytes))(module, module.exports, noRequire);
    if (!module.exports.compileComponents) throw new Error("component-compiler.js has no compileComponents");
    return module.exports.compileComponents;
  })();
  compiler.catch(() => (compiler = undefined));
  return compiler;
}

const decode = (bytes: Uint8Array) => new TextDecoder().decode(bytes);

export class UserComponentManager implements vscode.Disposable {
  private readonly onDidChangeEmitter = new vscode.EventEmitter<void>();
  /** Fires after every rebuild: panels reload their HTML and diagnostics. */
  readonly onDidChange = this.onDidChangeEmitter.event;

  private readonly scripts = new BuiltScripts();
  private readonly disposables: vscode.Disposable[] = [];
  private readonly byPanel = new Map<vscode.WebviewPanel, Set<string>>();
  private pendingNames = new Set<string>();
  private diagnostics: ComponentDiagnostic[] = [];
  /** The files the last build used: a change to one rebuilds. */
  private watched = new Set<string>();
  private version = 0;
  private lastCode = "";
  private building = false;
  private rebuildQueued = false;
  private debounce?: ReturnType<typeof setTimeout>;

  constructor(private readonly context: vscode.ExtensionContext) {
    this.disposables.push(vscode.workspace.registerFileSystemProvider(SCHEME, this.scripts, { isReadonly: true }));
    const watcher = vscode.workspace.createFileSystemWatcher("**/*");
    const changed = (uri: vscode.Uri) => {
      if (!this.pendingNames.size) return;
      const name = uri.path.split("/").pop() ?? "";
      const component = name.endsWith(".svelte") && this.pendingNames.has(name.slice(0, -".svelte".length));
      if (component || this.watched.has(uri.path)) this.scheduleBuild();
    };
    this.disposables.push(watcher, watcher.onDidChange(changed), watcher.onDidCreate(changed), watcher.onDidDelete(changed));
  }

  /** The built script for the mimic webview, once there is one. */
  bundleUri(): vscode.Uri | undefined {
    return this.version ? vscode.Uri.from({ scheme: SCHEME, path: `/user-components.${this.version}.js` }) : undefined;
  }

  bundleVersion(): number {
    return this.version;
  }

  /** Svelte and the kit, which the built script runs on. */
  runtimeUri(): vscode.Uri {
    return vscode.Uri.joinPath(this.context.extensionUri, "media", "dist", "user-runtime.js");
  }

  lastDiagnostics(): ComponentDiagnostic[] {
    return this.diagnostics;
  }

  get resourceRoot(): vscode.Uri {
    return vscode.Uri.from({ scheme: SCHEME, path: "/" });
  }

  registerPanel(panel: vscode.WebviewPanel): void {
    this.byPanel.set(panel, new Set());
    panel.onDidDispose(() => {
      this.byPanel.delete(panel);
      this.recomputeAndSchedule();
    });
  }

  request(panel: vscode.WebviewPanel, names: Iterable<string>): void {
    this.byPanel.set(panel, new Set(customComponentNames(names, BUILTIN_COMPONENTS)));
    this.recomputeAndSchedule();
  }

  activateTrust(): void {}

  private recomputeAndSchedule(): void {
    const union = new Set<string>();
    for (const s of this.byPanel.values()) for (const n of s) union.add(n);
    if (union.size === this.pendingNames.size && [...union].every((n) => this.pendingNames.has(n))) return;
    this.pendingNames = union;
    this.scheduleBuild();
  }

  private scheduleBuild(): void {
    if (this.debounce) clearTimeout(this.debounce);
    this.debounce = setTimeout(() => void this.rebuild(), DEBOUNCE_MS);
  }

  /** name -> the project's {name}.svelte, as a desktop finds it. */
  private async locate(names: string[]): Promise<Map<string, vscode.Uri>> {
    const found = new Map<string, vscode.Uri>();
    for (const name of names) {
      const uri = await locateComponentSource(name);
      if (uri) found.set(name, uri);
    }
    return found;
  }

  private async rebuild(): Promise<void> {
    if (this.building) {
      this.rebuildQueued = true;
      return;
    }
    this.building = true;
    let unchanged = false;
    try {
      const names = [...this.pendingNames].sort();
      if (!names.length) return;
      const located = await this.locate(names);
      const missing: ComponentDiagnostic[] = names
        .filter((n) => !located.has(n))
        .map((n) => ({ component: n, message: `no ${n}.svelte found in this project` }));
      const compile = await loadCompiler(this.context.extensionUri);
      // What is typed but not saved yet counts.
      const sources: Record<string, string> = {};
      for (const doc of vscode.workspace.textDocuments) if (doc.isDirty && doc.uri.scheme === "file") sources[doc.uri.path] = doc.getText();
      const read = async (p: string) => {
        try {
          return decode(await vscode.workspace.fs.readFile(vscode.Uri.file(p)));
        } catch {
          return undefined;
        }
      };
      const result = await compile(Object.fromEntries([...located].map(([n, uri]) => [n, uri.path])), read, { sources });

      const diagnostics = [...missing, ...result.diagnostics];
      this.watched = new Set(result.files);
      // A save that changes nothing built (autosave, a comment in a file
      // nothing imports) does not reload the open mimics.
      if (result.code === this.lastCode && JSON.stringify(diagnostics) === JSON.stringify(this.diagnostics)) {
        unchanged = true;
        return;
      }
      this.lastCode = result.code;
      this.diagnostics = diagnostics;
      const previous = this.bundleUri();
      this.version++;
      this.scripts.files.set(this.bundleUri()!.path, new TextEncoder().encode(result.code));
      if (previous) this.scripts.files.delete(previous.path);
    } catch (err) {
      this.diagnostics = [{ component: "*", message: "building custom components: " + describeError(err) }];
    } finally {
      this.building = false;
      if (!unchanged) this.onDidChangeEmitter.fire();
      if (this.rebuildQueued) {
        this.rebuildQueued = false;
        void this.rebuild();
      }
    }
  }

  dispose(): void {
    if (this.debounce) clearTimeout(this.debounce);
    for (const d of this.disposables) d.dispose();
    this.onDidChangeEmitter.dispose();
  }
}

/** The runtime, then the built components (and the kit's styles). */
export function userComponentScriptTag(webview: vscode.Webview, userComponents: UserComponentManager, nonce: string): string {
  const bundle = userComponents.bundleUri();
  if (!bundle) return "";
  const runtime = webview.asWebviewUri(userComponents.runtimeUri());
  const styles = runtime.toString().replace(/\.js$/, ".css");
  return (
    `\n<link rel="stylesheet" href="${styles}">` +
    `\n<script nonce="${nonce}" src="${runtime}"></script>` +
    `\n<script nonce="${nonce}" src="${webview.asWebviewUri(bundle)}"></script>`
  );
}
