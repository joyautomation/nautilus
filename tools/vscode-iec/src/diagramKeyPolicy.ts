// Pure rules behind the diagram surfaces' keyboard + title-bar commands, so
// they're unit-tested without vscode. See diagramKeys.ts for the host side
// and webview-ui/src/keyForward.ts for the webview side.

export type DiagramKeyAction = "undo" | "redo" | "save";

export function isDiagramKeyMessage(msg: unknown): msg is { type: "diagramKey"; action: DiagramKeyAction } {
  const m = msg as { type?: unknown; action?: unknown } | null;
  return (
    !!m && m.type === "diagramKey" && (m.action === "undo" || m.action === "redo" || m.action === "save")
  );
}

/** Whether a preview panel applies a forwarded key to its source document.
 * A diff is a read-only review: undo there would silently rewrite the
 * working tree underneath the overlay, so it's refused with a hint. The same
 * for an L5X, whose diagram is read-only. Save is always harmless. */
export function diagramKeyVerdict(
  action: DiagramKeyAction,
  state: { diffing: boolean; readOnly?: boolean }
): "apply" | "refuseDiff" | "refuseReadOnly" {
  if (action === "save") return "apply";
  if (state.diffing) return "refuseDiff";
  if (state.readOnly) return "refuseReadOnly";
  return "apply";
}

/** The diagram custom editor for a text document's language, if any. */
export function diagramViewTypeFor(languageId: string): string | undefined {
  switch (languageId) {
    case "iec-fbd":
      return "nautilus.fbdDiagram";
    case "iec-ld":
      return "nautilus.ldDiagram";
    case "iec-sfc":
      return "nautilus.sfcDiagram";
    case "logix-l5x":
      return "nautilus.l5xDiagram";
    default:
      return undefined;
  }
}
