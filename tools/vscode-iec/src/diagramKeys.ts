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
import { resolveSourceDoc } from "./sourceDoc";

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

/** Keys apply one at a time: each undo re-focuses the text and hands focus
 * back, and a held-down Ctrl+Z must not interleave two of those. */
let queue: Promise<void> = Promise.resolve();

export function applyDiagramKey(
  action: DiagramKeyAction,
  doc: vscode.TextDocument,
  panel: vscode.WebviewPanel,
  state: { diffing: boolean; readOnly?: boolean }
): Promise<void> {
  queue = queue.then(() => apply(action, doc, panel, state)).catch(() => undefined);
  return queue;
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
    await vscode.commands.executeCommand(action);
  } finally {
    panel.reveal(panel.viewColumn, false);
  }
}
