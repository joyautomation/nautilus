// The browser build's previewBuild.ts: the component preview compiled with
// the playground's Svelte (web/componentCompiler.ts, the compiler the
// mimic editor's custom components already use here), the script kept in
// memory under the nautilus-components: scheme, and Svelte and the kit
// loaded once in the pane's <head> (media/dist/user-runtime.js), since a
// browser-built bundle runs on window.__NX_RT__ rather than carrying its
// own. Same exports as ../previewBuild.ts.

import * as vscode from "vscode";
import type { ComponentDiagnostic } from "../userComponentEntry";
import { loadCompiler, type UserComponentManager } from "./userComponents";

export type PreviewBuildResult = { built: string[]; diagnostics: ComponentDiagnostic[] };

const decode = (bytes: Uint8Array) => new TextDecoder().decode(bytes);

export class PreviewBuilder {
  readonly resourceRoot: vscode.Uri;
  /** Each good build gets a new path, so the pane's appended <script>
   * never reads a cached older one. */
  private path = "";
  private generation = 0;

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly userComponents: UserComponentManager
  ) {
    this.resourceRoot = userComponents.resourceRoot;
  }

  headTags(webview: vscode.Webview, nonce: string): string {
    const runtime = webview.asWebviewUri(this.userComponents.runtimeUri()).toString();
    const styles = runtime.replace(/\.js$/, ".css");
    return `<link rel="stylesheet" href="${styles}">\n<script nonce="${nonce}" src="${runtime}"></script>`;
  }

  async build(name: string, fsPath: string, sources: Record<string, string>): Promise<PreviewBuildResult> {
    const compile = await loadCompiler(this.context.extensionUri);
    const read = async (p: string) => {
      if (p in sources) return sources[p];
      try {
        return decode(await vscode.workspace.fs.readFile(vscode.Uri.file(p)));
      } catch {
        return undefined;
      }
    };
    const result = await compile({ [name]: fsPath }, read, { sources });
    if (result.built.includes(name)) {
      const next = `/component-preview.${++this.generation}.js`;
      this.userComponents.putScript(next, result.code);
      if (this.path) this.userComponents.putScript(this.path, null);
      this.path = next;
    }
    return { built: result.built, diagnostics: result.diagnostics };
  }

  bundleSrc(webview: vscode.Webview): string {
    return webview.asWebviewUri(vscode.Uri.from({ scheme: "nautilus-components", path: this.path })).toString();
  }
}
