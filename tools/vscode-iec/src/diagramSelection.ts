// The tag selected in each diagram webview (ladder, FBD), so the
// commands that act on "the tag here" (Set Live Value, Force, Unforce) act on
// the diagram's selection when a diagram has focus, the way they act on the
// identifier under the cursor in text.
import type * as vscode from "vscode";

type Panel = Pick<vscode.WebviewPanel, "active" | "onDidDispose">;

const selected = new Map<Panel, string | undefined>();

/** A webview's `{type: "diagramSelection", tag}` message: remember it for
 * that panel. Returns true when msg was one (the caller is done with it). */
export function trackDiagramSelection(panel: Panel, msg: unknown): boolean {
  const m = msg as { type?: unknown; tag?: unknown } | undefined;
  if (m?.type !== "diagramSelection") return false;
  if (!selected.has(panel)) panel.onDidDispose(() => selected.delete(panel));
  selected.set(panel, typeof m.tag === "string" && m.tag ? m.tag : undefined);
  return true;
}

/** The tag selected in the diagram that has focus, if one does. */
export function activeDiagramTag(): string | undefined {
  for (const [panel, tag] of selected) if (panel.active) return tag;
  return undefined;
}

/** True while a diagram webview has focus (its selection, or none, is what
 * "the tag here" means then, not a text cursor elsewhere). */
export function diagramHasFocus(): boolean {
  for (const panel of selected.keys()) if (panel.active) return true;
  return false;
}
