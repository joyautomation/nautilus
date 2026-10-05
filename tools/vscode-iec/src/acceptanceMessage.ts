// The text of an acceptance-test failure in the Test Explorer, kept free of
// the vscode module so it can be unit-tested.
//
// VS Code shows only the FIRST line of a TestMessage inline (and as the
// peek's title), so that line has to be the answer: the tag and the value
// that broke, `PumpRun = false, want true`. Where and when it broke — the
// step, its line, the virtual time — is the second line, and the
// trajectory follows in the peek.

/** One test as `naut test -list` reports it. */
export interface Listed {
  suite: string;
  name: string;
  line: number;
}

/** A failure as `naut test -json` reports it. */
export interface Failure {
  step: number;
  /** The assertion that broke (the tag key or expression inside `expect:`
   * / `always:`, or the `alarms:` key). Older CLIs reported the step's
   * line here, which is still a sensible place to anchor. */
  line: number;
  /** The first line of the failing step; absent from older CLIs. */
  stepLine?: number;
  atMs: number;
  reason: string;
  detail?: string;
  trace?: { name: string; unit?: string; desc?: string; atMs: number[]; values: string[] }[];
}

/** One result as `naut test -json` reports it. */
export interface RunResult extends Listed {
  passed: boolean;
  scans: number;
  elapsedMs: number;
  failure?: Failure;
}

export interface FailureText {
  /** The TestMessage body; its first line is what shows inline. */
  text: string;
  /** 1-based line to anchor the message at, when the CLI named one. */
  line?: number;
}

/** Virtual time as a person reads it in a failure: seconds, ms resolution. */
function vtime(ms: number): string {
  return `t=${(ms / 1000).toFixed(3)}s`;
}

/** Render a failed result: the value that broke, then where and when, then
 * what the traced tags did. */
export function failureText(r: RunResult): FailureText {
  const f = r.failure;
  if (!f) return { text: "failed" };

  const lines: string[] = [];
  // Line 1, shown inline: the assertion and the value it saw. A failure
  // with no detail (a scan that faulted with no message) leads with its
  // reason instead.
  lines.push(f.detail || f.reason);

  // Line 2: where in the test and when in virtual time — and why, when
  // the reason adds something the detail does not ("never held within
  // 1s" says more than the value alone).
  const where: string[] = [];
  if (f.step > 0) {
    const stepLine = f.stepLine && f.stepLine !== f.line ? ` (line ${f.stepLine})` : "";
    where.push(`step ${f.step}${stepLine}`);
  }
  where.push(`${vtime(f.atMs)} of virtual time`);
  let second = where.join(", ");
  if (f.detail) second += ` — ${f.reason}`;
  lines.push(second);

  for (const t of f.trace ?? []) {
    const head = [t.name, t.unit, t.desc && `— ${t.desc}`].filter(Boolean).join("  ");
    lines.push("", head);
    lines.push(t.atMs.map((ms) => `${(ms / 1000).toFixed(2)}s`.padStart(11)).join(""));
    lines.push(t.values.map((v) => v.padStart(11)).join(""));
  }
  return { text: lines.join("\n"), line: f.line > 0 ? f.line : undefined };
}
