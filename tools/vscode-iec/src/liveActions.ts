// The parts of the live tag commands (Set Live Value, Toggle Bit, Force,
// Unforce) that need no vscode: which tag a command was invoked for, and
// whether Toggle Bit may write. liveValues.ts wires them to the workbench.

/** What a tag command is invoked with: a tag name (a hover link), a Live
 * Values tree item ({tag} / {name}), or a diagram element's right-click
 * context ({nautilusTag}, data-vscode-context in the webview). */
export type TagArg = string | { tag?: string; name?: string; nautilusTag?: string } | undefined;

/** The tag a command argument names, if it names one. */
export function tagOf(arg: TagArg): string | undefined {
  if (typeof arg === "string") return arg || undefined;
  return arg?.tag ?? arg?.name ?? arg?.nautilusTag ?? undefined;
}

/** The warning when a command has no tag to act on: a diagram with focus
 * wants a selected element, a text editor a cursor on a tag. */
export function noTagMessage(diagramFocused: boolean, action: string, textHint = "a tag"): string {
  return diagramFocused
    ? `nautilus: select a contact or coil in the diagram, then ${action}`
    : `nautilus: put the cursor on ${textHint}, then ${action}`;
}

/** What Toggle Bit should do with a tag. */
export type ToggleDecision =
  | { write: boolean; shown: "TRUE" | "FALSE" }
  | { refuse: string };

/**
 * Toggle Bit writes the opposite of a BOOL tag's live value. It refuses a
 * tag declared as something else, one with no live value yet, one whose live
 * value is not a boolean, and a forced one (the force would win next scan).
 *
 * `type`: the declared type (e.g. "BOOL", "INT"), when known; `typeLabel`
 * how to name it. `shown`: the live value as the user sees it.
 */
export function toggleBitDecision(
  name: string,
  t: { type?: string; typeLabel?: string; current: unknown; shown?: string; forced: boolean },
): ToggleDecision {
  if (t.type && t.type.toUpperCase() !== "BOOL") {
    return { refuse: `nautilus: Toggle Bit works on BOOL tags; ${name} is ${t.typeLabel ?? t.type}` };
  }
  if (typeof t.current !== "boolean") {
    return {
      refuse: t.current === undefined
        ? `nautilus: no live value for ${name} yet (is the controller running?)`
        : `nautilus: Toggle Bit works on BOOL tags; ${name} is ${t.shown ?? String(t.current)}`,
    };
  }
  if (t.forced) {
    return { refuse: `nautilus: ${name} is forced, so a toggle would not stick; Unforce it first` };
  }
  return { write: !t.current, shown: !t.current ? "TRUE" : "FALSE" };
}
