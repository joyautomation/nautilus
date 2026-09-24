// The FBD / Ladder / SFC preview panels follow the active editor, like the
// markdown preview. Their diff state (vs git HEAD, vs the controller, or
// between two revisions) is a frozen base that belongs to ONE document —
// so it must survive re-focusing that same document (clicking into its
// text editor to make the edit you're diffing) and must be dropped the
// moment the preview moves to a different file (or file B would be
// overlaid on file A's base). One rule, shared by all three hosts; pure,
// so it's unit-tested without vscode.

/** Where the preview goes when `nextUri` becomes the active document:
 * the same URI keeps the diff; a different one leaves diff mode. */
export function followActiveDoc<B>(
  current: { docUri?: string; diffBase?: B },
  nextUri: string
): { docUri: string; diffBase?: B } {
  if (current.docUri === nextUri) return { docUri: nextUri, diffBase: current.diffBase };
  return { docUri: nextUri, diffBase: undefined };
}
