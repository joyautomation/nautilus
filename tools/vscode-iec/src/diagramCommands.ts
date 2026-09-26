// Title-bar commands that move between the text and the diagram editor of
// an .fbd / .ld / .sfc / .L5X file. Text stays the default editor for these
// files (it's the source of truth and what git, the language server and code
// review see); these make the diagram editor one click away instead of
// behind "Reopen Editor With…".

import * as vscode from "vscode";
import { diagramViewTypeFor } from "./diagramKeyPolicy";

/** The URI a title-bar command acts on: VS Code passes the editor's resource
 * when the button is clicked; from the palette, use the active tab. */
function targetUri(arg: unknown): vscode.Uri | undefined {
  if (arg instanceof vscode.Uri) return arg;
  const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
  if (input instanceof vscode.TabInputText || input instanceof vscode.TabInputCustom) return input.uri;
  return vscode.window.activeTextEditor?.document.uri;
}

/** Text editor → the diagram custom editor, in place (like "Reopen Editor
 * With…", which also carries unsaved changes over — both editors share the
 * one TextDocument). */
async function openAsDiagram(arg: unknown): Promise<void> {
  const uri = targetUri(arg);
  if (!uri) return;
  const doc = await vscode.workspace.openTextDocument(uri);
  const viewType = diagramViewTypeFor(doc.languageId);
  if (!viewType) {
    void vscode.window.showErrorMessage("nautilus: no diagram editor for this file");
    return;
  }
  const active = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
  // The command stays in the palette even when the active tab is already
  // this exact diagram (see package.json's commandPalette `when`) — hiding
  // it there left the palette's fuzzy matcher landing on "Open … Diagram
  // Preview" instead when a user re-typed the command by habit (it splits
  // the editor area, a surprise). A no-op with a status message beats that
  // trap without reintroducing it.
  if (
    active instanceof vscode.TabInputCustom &&
    active.viewType === viewType &&
    active.uri.toString() === uri.toString()
  ) {
    void vscode.window.setStatusBarMessage("nautilus: already open as a diagram", 3000);
    return;
  }
  if (active instanceof vscode.TabInputText && active.uri.toString() === uri.toString()) {
    try {
      // Replaces the active text tab rather than opening a second one.
      await vscode.commands.executeCommand("reopenActiveEditorWith", viewType);
      return;
    } catch {
      // Older VS Code without the command: open alongside instead.
    }
  }
  await vscode.commands.executeCommand("vscode.openWith", uri, viewType);
}

/** Diagram editor → its text, side by side, so both stay in view. */
async function showSource(arg: unknown): Promise<void> {
  const uri = targetUri(arg);
  if (!uri) return;
  await vscode.commands.executeCommand("vscode.openWith", uri, "default", vscode.ViewColumn.Beside);
}

export function registerDiagramCommands(): vscode.Disposable {
  return vscode.Disposable.from(
    vscode.commands.registerCommand("nautilus.diagram.openAsDiagram", openAsDiagram),
    vscode.commands.registerCommand("nautilus.diagram.showSource", showSource)
  );
}
