// Pure (vscode-free) formatting for the FB monitor CodeLens, so it can be
// unit-tested.

/**
 * Title of the lens over a FUNCTION_BLOCK. `inst` is the monitored instance
 * ("" when none), `candidates` the declared instances of that type. With more
 * than one candidate the title says the monitored instance's real position
 * ("2 of 2"); if it is not among the candidates, it falls back to a count.
 */
export function fbMonitorTitle(inst: string, candidates: string[]): string {
  if (!inst) return "○ live values: monitor an instance…";
  const n = candidates.length;
  if (n <= 1) return `◉ live values: monitoring ${inst}`;
  const i = candidates.indexOf(inst);
  const where = i >= 0 ? `${i + 1} of ${n}` : `${n} instances`;
  return `◉ live values: monitoring ${inst} — ${where}, click to switch`;
}
