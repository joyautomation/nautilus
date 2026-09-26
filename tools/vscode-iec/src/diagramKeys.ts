// Undo / redo / save pressed inside a diagram PREVIEW panel.
//
// The preview is a plain WebviewPanel over no document, so VS Code's own
// handling does nothing useful there: `undo` runs the iframe's native undo
// (a no-op on a diagram) and Ctrl+S has nothing to save. The webview posts
// the key instead (webview-ui/src/keyForward.ts) and it lands here, applied
// to the .fbd/.ld/.sfc TextDocument the preview shows — so it shares the
// text editor's undo stack exactly, and the diagram re-renders from the text
// like after any other change.
//
// The diagram CUSTOM editors don't come through here: VS Code routes
// Ctrl+Z / Ctrl+S in a custom text editor to its document already.

import * as vscode from "vscode";
import { diagramKeyVerdict, isDiagramKeyMessage, type DiagramKeyAction } from "./diagramKeyPolicy";
import { resolveSourceDoc, serialQueue } from "./sourceDoc";

export { isDiagramKeyMessage };
export { serialQueue } from "./sourceDoc";

/** The TextDocument a preview shows, by URI — open in an editor or not.
 * Never `workspace.textDocuments` alone: that forgets a document once its
 * last editor closes, and the preview's gestures then went nowhere. */
export function sourceDocument(uri: vscode.Uri | undefined): Promise<vscode.TextDocument | undefined> {
  return resolveSourceDoc(uri?.toString(), vscode.workspace.textDocuments, () =>
    vscode.workspace.openTextDocument(uri!)
  );
}

/** Keys apply strictly one at a time, in arrival order: each undo/redo
 * re-focuses the text, runs the command, waits for the edit to actually
 * land, and hands focus back — and the NEXT press (a held-down Ctrl+Z, or
 * a save right behind it) must not start until all of that has finished.
 * Built on the same serialQueue() the preview panels use for their own
 * message ordering, so callers awaiting the returned promise really do
 * wait for the action to be fully applied, not just scheduled. */
const runInOrder = serialQueue();

export function applyDiagramKey(
  action: DiagramKeyAction,
  doc: vscode.TextDocument,
  panel: vscode.WebviewPanel,
  state: { diffing: boolean; readOnly?: boolean }
): Promise<void> {
  return runInOrder(() => apply(action, doc, panel, state));
}

async function apply(
  action: DiagramKeyAction,
  doc: vscode.TextDocument,
  panel: vscode.WebviewPanel,
  state: { diffing: boolean; readOnly?: boolean }
): Promise<void> {
  switch (diagramKeyVerdict(action, state)) {
    case "refuseDiff":
      vscode.window.setStatusBarMessage(`nautilus: the diff is read-only — ✕ exit diff to ${action}`, 4000);
      return;
    case "refuseReadOnly":
      vscode.window.setStatusBarMessage(`nautilus: this diagram is read-only — ${action} in the text`, 4000);
      return;
  }
  if (action === "save") {
    // TextDocument.save needs no focus and no active editor.
    await doc.save();
    return;
  }
  // There's no API to undo a document directly: the `undo` command acts on
  // the FOCUSED editor (and while the webview holds focus, on the webview).
  // So focus the document's text editor — the visible one if there is one,
  // otherwise open it beside — undo there, and hand focus back.
  const uri = doc.uri.toString();
  const visible = vscode.window.visibleTextEditors.find((e) => e.document.uri.toString() === uri);
  await vscode.window.showTextDocument(doc, {
    viewColumn: visible?.viewColumn ?? vscode.ViewColumn.Beside,
    preserveFocus: false,
    preview: false,
  });
  try {
    // Never undo some other file because focus didn't land where expected.
    if (vscode.window.activeTextEditor?.document.uri.toString() !== uri) return;
    const versionBeforeCommand = doc.version;
    await vscode.commands.executeCommand(action);
    // `executeCommand` resolves once the command has been dispatched to the
    // editor, not once this extension-host TextDocument's own copy has
    // caught up with the edit — that arrives separately, as an
    // onDidChangeTextDocument sync. Without this wait, a save queued right
    // behind (through this same queue) could call doc.save() before the
    // sync lands and write the PRE-undo text, while the edit still applies
    // moments later and leaves the document dirty again.
    await waitForDocSync(doc, versionBeforeCommand);
  } finally {
    panel.reveal(panel.viewColumn, false);
  }
}

/** Resolves once `doc`'s version has moved past `before` (the edit has
 * synced to this extension host), or after `timeoutMs` if nothing changed
 * — an undo/redo with an empty stack is a legitimate no-op, so this must
 * not stall every empty-stack press waiting for a sync that never comes. */
function waitForDocSync(doc: vscode.TextDocument, before: number, timeoutMs = 300): Promise<void> {
  if (doc.version !== before) return Promise.resolve();
  const uri = doc.uri.toString();
  return new Promise((resolve) => {
    const finish = () => {
      sub.dispose();
      clearTimeout(timer);
      resolve();
    };
    const sub = vscode.workspace.onDidChangeTextDocument((e) => {
      if (e.document.uri.toString() === uri && e.document.version !== before) finish();
    });
    const timer = setTimeout(finish, timeoutMs);
  });
}
