// Title-bar commands that move between the text and the diagram editor of
// an .fbd / .ld / .sfc / .L5X file. Text stays the default editor for these
// files (it's the source of truth and what git, the language server and code
// review see); these make the diagram editor one click away instead of
// behind "Reopen Editor With…".

import * as vscode from "vscode";
import { diagramViewTypeFor } from "./diagramKeyPolicy";
import { afterTextTabClosed, nextSnapshot, GuardedDoc } from "./sourceGuard";

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
  const doc = await vscode.workspace.openTextDocument(uri);
  guard(doc);
  await vscode.commands.executeCommand("vscode.openWith", uri, "default", vscode.ViewColumn.Beside);
}

// ── Show Source guard (see sourceGuard.ts and issue #117) ─────────────────
// Documents whose text tab was opened by Show Source, keyed by URI string,
// with the last dirty text seen. Entries live until the text tab is gone.

const guarded = new Map<string, GuardedDoc | undefined>();

function guard(doc: vscode.TextDocument): void {
  const key = doc.uri.toString();
  guarded.set(key, nextSnapshot(guarded.get(key), doc.isDirty, doc.getText()));
}

function tabOpenFor(uri: string, kind: "text" | "custom"): boolean {
  return vscode.window.tabGroups.all.some((g) =>
    g.tabs.some((t) => {
      const i = t.input;
      if (kind === "text") return i instanceof vscode.TabInputText && i.uri.toString() === uri;
      return i instanceof vscode.TabInputCustom && i.uri.toString() === uri;
    })
  );
}

async function onTextTabClosed(uri: string): Promise<void> {
  // The confirm → revert → close sequence lands in either order relative to
  // this event; give the document a beat to settle before judging.
  await new Promise((r) => setTimeout(r, 150));
  const doc = vscode.workspace.textDocuments.find((d) => d.uri.toString() === uri);
  const snap = guarded.get(uri);
  const verdict = afterTextTabClosed(snap, {
    docOpen: !!doc && !doc.isClosed,
    diagramOpen: tabOpenFor(uri, "custom"),
    isDirty: !!doc?.isDirty,
    text: doc?.getText() ?? "",
  });
  if (verdict === "keep") return;
  if (verdict === "restore" && doc && snap) {
    const whole = new vscode.Range(doc.positionAt(0), doc.positionAt(doc.getText().length));
    const edit = new vscode.WorkspaceEdit();
    edit.replace(doc.uri, whole, snap.text);
    if (await vscode.workspace.applyEdit(edit)) {
      void vscode.window.setStatusBarMessage(
        "nautilus: closed the source view — the diagram keeps its unsaved edits",
        6000
      );
    }
  }
  if (!tabOpenFor(uri, "text")) guarded.delete(uri);
}

function registerSourceGuard(): vscode.Disposable {
  return vscode.Disposable.from(
    vscode.workspace.onDidChangeTextDocument((e) => {
      const key = e.document.uri.toString();
      if (guarded.has(key)) guard(e.document);
    }),
    vscode.workspace.onDidCloseTextDocument((d) => guarded.delete(d.uri.toString())),
    vscode.window.tabGroups.onDidChangeTabs((e) => {
      for (const t of e.closed) {
        if (t.input instanceof vscode.TabInputText) {
          const key = t.input.uri.toString();
          if (guarded.has(key)) void onTextTabClosed(key);
        }
      }
    })
  );
}

export function registerDiagramCommands(): vscode.Disposable {
  return vscode.Disposable.from(
    vscode.commands.registerCommand("nautilus.diagram.openAsDiagram", openAsDiagram),
    vscode.commands.registerCommand("nautilus.diagram.showSource", showSource),
    registerSourceGuard()
  );
}
