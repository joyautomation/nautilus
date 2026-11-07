// The web build's userComponents.ts. On a desktop the mimic editor renders a
// project's own `{Name}.svelte` components by compiling them with the
// project's svelte and esbuild (userComponentBuild.ts), which a browser does
// not have. Here the kit's built-in equipment renders for real and a custom
// component keeps the editor's placeholder chip, whose tooltip says why.
// Same exports as ../userComponents.ts, so mimicEditor.ts is unchanged.
import * as vscode from "vscode";
import { BUILTIN_COMPONENT_NAMES } from "../builtinComponents";
import type { ComponentDiagnostic } from "../userComponentBuild";

export const BUILTIN_COMPONENTS = new Set(BUILTIN_COMPONENT_NAMES);

const DESKTOP_ONLY =
  "custom components render in VS Code with the nautilus extension; the playground draws the built-in equipment";

export class UserComponentManager implements vscode.Disposable {
  private readonly onDidChangeEmitter = new vscode.EventEmitter<void>();
  readonly onDidChange = this.onDidChangeEmitter.event;
  private readonly byPanel = new Map<vscode.WebviewPanel, string[]>();

  constructor(private readonly context: vscode.ExtensionContext) {}

  bundleUri(): vscode.Uri | undefined {
    return undefined;
  }

  bundleVersion(): number {
    return 0;
  }

  lastDiagnostics(): ComponentDiagnostic[] {
    const names = new Set<string>();
    for (const list of this.byPanel.values()) for (const n of list) names.add(n);
    return [...names].sort().map((component) => ({ component, message: DESKTOP_ONLY }));
  }

  get resourceRoot(): vscode.Uri {
    return this.context.extensionUri;
  }

  registerPanel(panel: vscode.WebviewPanel): void {
    this.byPanel.set(panel, []);
    panel.onDidDispose(() => this.byPanel.delete(panel));
  }

  request(panel: vscode.WebviewPanel, names: Iterable<string>): void {
    const custom = [...new Set(names)].filter((n) => n && !BUILTIN_COMPONENTS.has(n));
    const before = (this.byPanel.get(panel) ?? []).join();
    this.byPanel.set(panel, custom);
    // Diagnostics reach the panel on its ready handshake and on onDidChange;
    // firing it reloads the panel's HTML, so only when the set changed.
    if (custom.join() !== before && custom.length) {
      void panel.webview.postMessage({ type: "userComponentDiagnostics", diagnostics: this.lastDiagnostics() });
    }
  }

  activateTrust(): void {}

  dispose(): void {
    this.onDidChangeEmitter.dispose();
  }
}

export function userComponentScriptTag(): string {
  return "";
}
