// Test hooks: a JSON snapshot of the extension's observable state, for the
// whole-VS-Code rig (tools/rig) to assert against. VS Code's API has no test
// ids and no way to read a status bar or a notification from outside, so the
// extension writes down what it knows.
//
// Opt-in: nothing happens unless NAUTILUS_TEST_STATE=<file> is set at
// activation. This module is pure (no `vscode` import) so the writer is unit
// tested; extension.ts wires the live sources into it.

import * as fs from "fs";
import * as path from "path";

/** Bumped when a field changes meaning or is removed (adding is free). */
export const TEST_STATE_VERSION = 1;

/** Each status-bar item has a stable `name` and a tooltip that starts with
 * `[<name>]`, so a probe can find it by either. */
export interface StatusBarEntry {
  name: string;
  text: string;
  tooltip: string;
}

export interface OpenEditor {
  uri: string;
  viewType: string;
}

export interface TestSnapshot {
  version: number;
  cliPath: string;
  cliVersion: string;
  runtimeUrl: string;
  connected: boolean;
  syncState: string;
  liveValuesEnabled: boolean;
  openEditors: OpenEditor[];
  statusBar: StatusBarEntry[];
  lastNotification: { level: string; text: string } | null;
  lastError: string;
  /** NAUTILUS_TEST_FAST was set (the extension has no cosmetic delays to skip today). */
  fast: boolean;
}

export function emptySnapshot(): TestSnapshot {
  return {
    version: TEST_STATE_VERSION,
    cliPath: "",
    cliVersion: "",
    runtimeUrl: "",
    connected: false,
    syncState: "unknown",
    liveValuesEnabled: false,
    openEditors: [],
    statusBar: [],
    lastNotification: null,
    lastError: "",
    fast: false,
  };
}

/** Status-bar names. Stable: the rig finds items by these. */
export const STATUS_LIVE = "nautilus.live";
export const STATUS_SYNC = "nautilus.sync";
export const STATUS_FORCES = "nautilus.forces";

/** The tooltip carrying its machine-readable prefix. */
export function prefixedTooltip(name: string, tooltip: string): string {
  return `[${name}] ${tooltip}`;
}

export interface WriterDeps {
  write(file: string, data: string): void;
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(t: unknown): void;
}

const realDeps: WriterDeps = {
  write(file, data) {
    fs.mkdirSync(path.dirname(file), { recursive: true });
    // Rename into place so a reader never sees half a file.
    const tmp = file + ".tmp";
    fs.writeFileSync(tmp, data);
    fs.renameSync(tmp, file);
  },
  setTimer: (fn, ms) => setTimeout(fn, ms),
  clearTimer: (t) => clearTimeout(t as NodeJS.Timeout),
};

/** Holds the snapshot and writes it, debounced. Never throws. */
export class SnapshotWriter {
  private snap = emptySnapshot();
  private timer: unknown;
  writes = 0;

  constructor(
    private readonly file: string,
    private readonly debounceMs = 250,
    private readonly deps: WriterDeps = realDeps
  ) {}

  get current(): TestSnapshot {
    return this.snap;
  }

  /** Merge fields and schedule a write; bursts coalesce into one. */
  update(patch: Partial<TestSnapshot>): void {
    this.snap = { ...this.snap, ...patch };
    this.schedule();
  }

  /** Set (or with undefined, remove) one status-bar entry by name. */
  setStatus(name: string, entry: { text: string; tooltip: string } | undefined): void {
    const rest = this.snap.statusBar.filter((s) => s.name !== name);
    if (entry) rest.push({ name, text: entry.text, tooltip: prefixedTooltip(name, entry.tooltip) });
    rest.sort((a, b) => a.name.localeCompare(b.name));
    this.update({ statusBar: rest });
  }

  notify(level: "info" | "warning" | "error", text: string): void {
    this.update(level === "error" ? { lastNotification: { level, text }, lastError: text } : { lastNotification: { level, text } });
  }

  /** Write now (activation), cancelling any pending write. */
  flush(): void {
    if (this.timer !== undefined) this.deps.clearTimer(this.timer);
    this.timer = undefined;
    try {
      this.deps.write(this.file, JSON.stringify(this.snap, null, 2) + "\n");
      this.writes++;
    } catch {
      // a test hook must never take the extension down
    }
  }

  private schedule(): void {
    if (this.timer !== undefined) return; // a write is already pending; it will carry this change
    this.timer = this.deps.setTimer(() => {
      this.timer = undefined;
      this.flush();
    }, this.debounceMs);
  }
}

// ── process-wide hook ──────────────────────────────────────────────────────

let writer: SnapshotWriter | undefined;

/** Enable from the environment; returns the writer or undefined when off. */
export function initTestState(env: NodeJS.ProcessEnv = process.env): SnapshotWriter | undefined {
  const file = env.NAUTILUS_TEST_STATE;
  if (!file) return undefined;
  writer = new SnapshotWriter(file);
  writer.update({ fast: env.NAUTILUS_TEST_FAST === "1" });
  writer.flush();
  return writer;
}

/** The active writer, or undefined (every call site is `testState()?.…`). */
export function testState(): SnapshotWriter | undefined {
  return writer;
}
