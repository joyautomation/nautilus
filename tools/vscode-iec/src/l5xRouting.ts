// Which CLI verb renders a ladder, and whether the result is editable.
//
// The ladder view serves two completely different kinds of file — nautilus
// `.ld` source and Rockwell `.L5X` exports — and downstream they are
// indistinguishable, because `nautilus logix graph` emits exactly the model
// `nautilus ld graph` emits. The dispatch lives here, free of any vscode
// import, so it can be tested directly: get it wrong and an L5X is handed
// to `ld graph`, which reports a parse error on XML and reads like a broken
// reader rather than a wiring mistake.

/** Is this path a Rockwell L5X export? */
export function isL5X(pathOrUri: string | undefined): boolean {
  return !!pathOrUri && /\.l5x$/i.test(pathOrUri);
}

/** CLI arguments that render the source on stdin as a ladder model. */
export function graphArgs(at?: string): string[] {
  // An L5X carries its own context — the whole controller is in the file —
  // so it needs no `at` for library resolution. Passing one would be read
  // as a ROUTINE selector and silently select nothing.
  if (isL5X(at)) return ["logix", "graph", "-"];
  return at ? ["ld", "graph", "-", at] : ["ld", "graph", "-"];
}
