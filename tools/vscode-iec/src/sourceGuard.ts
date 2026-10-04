// Keeps a diagram editor's unsaved edits alive across "Show Source".
//
// The diagram (a CustomTextEditor) and the text editor opened by Show Source
// share one TextDocument, but VS Code sees two different editor inputs. When
// the text tab is the one being closed and the document is dirty, VS Code
// asks Save / Don't Save / Cancel even though the diagram still shows the
// file — and "Don't Save" REVERTS the document, wiping edits the user made
// in the diagram (issue #117, smoke check 04-title-buttons).
//
// We cannot suppress the prompt. We can make "Don't Save" mean "close the
// text view" and nothing more: remember the dirty text while the text tab is
// open and, if closing it reverted the document under a diagram that is
// still open, re-apply that text. This module is the pure decision logic;
// diagramCommands.ts wires it to the vscode API. No vscode import here so
// `node --test` covers it.

export interface GuardedDoc {
  /** The latest DIRTY text seen while the text tab was open. */
  text: string;
}

export interface CloseObservation {
  /** The document is still open (not disposed). */
  docOpen: boolean;
  /** A diagram (custom editor) tab for the same file is still open. */
  diagramOpen: boolean;
  /** `document.isDirty` after the text tab closed. */
  isDirty: boolean;
  /** `document.getText()` after the text tab closed. */
  text: string;
}

export type CloseVerdict = "restore" | "forget" | "keep";

/**
 * What to do after the Show Source text tab for a guarded document closed.
 *
 *  - `restore`: the document went clean with DIFFERENT text than the last
 *    dirty snapshot while a diagram still shows it — that is "Don't Save"
 *    reverting it under the diagram; put the snapshot back.
 *  - `forget`:  nothing to protect any more (document gone, no diagram left,
 *    saved — clean with the same text — or the user had already undone back
 *    to clean before closing: dirty snapshot never taken).
 *  - `keep`:    the document is still dirty (the user chose Cancel, or Save
 *    failed); keep guarding until the text tab really goes.
 */
export function afterTextTabClosed(snap: GuardedDoc | undefined, o: CloseObservation): CloseVerdict {
  if (!o.docOpen || !o.diagramOpen || !snap) return "forget";
  if (o.isDirty) return "keep";
  return o.text === snap.text ? "forget" : "restore";
}

/** While the text tab is open, the snapshot follows every DIRTY state of the
 * document and ignores clean ones: a clean document has nothing to restore,
 * and a revert must not overwrite the last dirty text with the on-disk text. */
export function nextSnapshot(snap: GuardedDoc | undefined, isDirty: boolean, text: string): GuardedDoc | undefined {
  return isDirty ? { text } : snap;
}
