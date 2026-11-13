// The component preview's build, on a desktop: the project's own svelte and
// esbuild (userComponentBuild.ts) write component-preview.js beside the
// shared user-components bundle, and the pane loads it from there. The
// browser build swaps this file for web/previewBuild.ts (esbuild.web.mjs),
// which compiles with the playground's Svelte and keeps the script in
// memory. Same exports, so componentPreview.ts is the same on both.

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import { buildUserComponentsBundle, type ComponentDiagnostic } from "./userComponentBuild";
import type { UserComponentManager } from "./userComponents";

const BUNDLE_FILE = "component-preview.js";

export type PreviewBuildResult = { built: string[]; diagnostics: ComponentDiagnostic[] };

export class PreviewBuilder {
  /** Where the pane may load scripts from. */
  readonly resourceRoot: vscode.Uri;
  private readonly bundleFsPath: string;

  constructor(_context: vscode.ExtensionContext, userComponents: UserComponentManager) {
    // Same workspace-storage directory as the shared user-components
    // bundle, so one localResourceRoots entry covers both.
    this.resourceRoot = userComponents.resourceRoot;
    this.bundleFsPath = vscode.Uri.joinPath(this.resourceRoot, BUNDLE_FILE).fsPath;
  }

  /** Tags the pane's <head> needs before any bundle: none here, the
   * desktop bundle carries its own Svelte. */
  headTags(_webview: vscode.Webview, _nonce: string): string {
    return "";
  }

  /** Compile `name` from `fsPath` (with `sources` standing in for unsaved
   * buffers). Only a bundle that has the component in it replaces the
   * last good one, so a compile error leaves the last render on screen. */
  async build(name: string, fsPath: string, sources: Record<string, string>): Promise<PreviewBuildResult> {
    await fs.promises.mkdir(path.dirname(this.bundleFsPath), { recursive: true });
    const staging = this.bundleFsPath + ".next";
    const result = await buildUserComponentsBundle({ [name]: fsPath }, staging, { sources });
    if (result.built.includes(name)) await fs.promises.rename(staging, this.bundleFsPath);
    return result;
  }

  /** The last good bundle, as the pane loads it. */
  bundleSrc(webview: vscode.Webview): string {
    return webview.asWebviewUri(vscode.Uri.file(this.bundleFsPath)).toString();
  }
}
