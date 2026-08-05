// Acceptance tests in the Test Explorer.
//
// `nautilus test` runs a project's *_test.yaml suites on a virtual clock,
// so a ten-second on-delay is asserted exactly, in milliseconds. This
// surfaces that as native VS Code testing: a tree per suite, run buttons
// in the gutter, and failures shown inline on the assertion that broke.
//
// Discovery shells out to `nautilus test -list` (which does not compile
// the project, so the tree survives a program that is mid-edit); running
// shells out to `nautilus test -json` and maps the NDJSON back onto the
// tree by (suite, name).

import * as vscode from "vscode";
import { execFile } from "node:child_process";
import * as path from "node:path";

const SUITE_GLOB = "**/*_test.yaml";

/** One test as `nautilus test -list` reports it. */
interface Listed {
  suite: string;
  name: string;
  line: number;
}

/** One result as `nautilus test -json` reports it. */
interface RunResult extends Listed {
  passed: boolean;
  scans: number;
  elapsedMs: number;
  failure?: {
    step: number;
    line: number;
    atMs: number;
    reason: string;
    detail?: string;
    trace?: { name: string; unit?: string; desc?: string; atMs: number[]; values: string[] }[];
  };
}

function cliPath(): string {
  return vscode.workspace.getConfiguration("nautilus").get<string>("cliPath") || "nautilus";
}

/** Run the CLI in `cwd` and return stdout, or throw with stderr attached. */
function runCli(cwd: string, args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(cliPath(), args, { cwd, maxBuffer: 8 * 1024 * 1024 }, (err, stdout, stderr) => {
      // `nautilus test` exits non-zero when tests FAIL, which is not an
      // error here — the JSON on stdout is exactly what we came for.
      if (stdout.trim()) return resolve(stdout);
      if (err) return reject(new Error(stderr.trim() || err.message));
      resolve(stdout);
    });
  });
}

function parseNdjson<T>(out: string): T[] {
  const rows: T[] = [];
  for (const line of out.split("\n")) {
    const s = line.trim();
    if (!s) continue;
    try {
      rows.push(JSON.parse(s) as T);
    } catch {
      // A stray non-JSON line (a warning, say) is not worth failing over.
    }
  }
  return rows;
}

/** Escape a test name for use inside the CLI's -run regexp. */
function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export class AcceptanceTests implements vscode.Disposable {
  private readonly ctrl: vscode.TestController;
  private readonly disposables: vscode.Disposable[] = [];
  /** test item id -> the folder its project lives in, and its raw name. */
  private readonly meta = new Map<string, { cwd: string; suite: string; name: string }>();

  constructor() {
    this.ctrl = vscode.tests.createTestController("nautilusAcceptance", "nautilus acceptance");
    this.disposables.push(this.ctrl);

    this.ctrl.refreshHandler = () => this.discoverAll();
    this.ctrl.createRunProfile(
      "Run",
      vscode.TestRunProfileKind.Run,
      (req, token) => this.run(req, token),
      true,
    );

    // Re-discover when a suite is written, created, or deleted — the tree
    // should track the file you are editing without a manual refresh.
    const watcher = vscode.workspace.createFileSystemWatcher(SUITE_GLOB);
    this.disposables.push(watcher);
    watcher.onDidCreate(() => this.discoverAll(), this, this.disposables);
    watcher.onDidDelete(() => this.discoverAll(), this, this.disposables);
    watcher.onDidChange(() => this.discoverAll(), this, this.disposables);
    vscode.workspace.onDidChangeWorkspaceFolders(() => this.discoverAll(), this, this.disposables);

    void this.discoverAll();
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
  }

  /** Project roots: every workspace folder holding a nautilus.yaml. */
  private async projectRoots(): Promise<string[]> {
    const manifests = await vscode.workspace.findFiles("**/nautilus.yaml", "**/node_modules/**");
    const roots = new Set<string>();
    for (const m of manifests) roots.add(path.dirname(m.fsPath));
    return [...roots].sort();
  }

  private async discoverAll(): Promise<void> {
    this.ctrl.items.replace([]);
    this.meta.clear();
    for (const root of await this.projectRoots()) {
      try {
        await this.discover(root);
      } catch {
        // A project that can't be listed (no suites, unreadable manifest)
        // simply contributes nothing; a popup per keystroke would be worse
        // than a quiet tree.
      }
    }
  }

  private async discover(root: string): Promise<void> {
    const listed = parseNdjson<Listed>(await runCli(root, ["test", "-list", "."]));
    for (const t of listed) {
      const file = vscode.Uri.file(path.join(root, t.suite));
      const suiteId = file.toString();
      let suiteItem = this.ctrl.items.get(suiteId);
      if (!suiteItem) {
        suiteItem = this.ctrl.createTestItem(suiteId, path.basename(t.suite), file);
        this.ctrl.items.add(suiteItem);
      }
      const id = `${suiteId}::${t.name}`;
      const item = this.ctrl.createTestItem(id, t.name, file);
      // -list reports 1-based lines; VS Code ranges are 0-based.
      item.range = new vscode.Range(Math.max(0, t.line - 1), 0, Math.max(0, t.line - 1), 0);
      suiteItem.children.add(item);
      this.meta.set(id, { cwd: root, suite: t.suite, name: t.name });
    }
  }

  /** Every leaf test the request asks for. */
  private requested(req: vscode.TestRunRequest): vscode.TestItem[] {
    const out: vscode.TestItem[] = [];
    const visit = (item: vscode.TestItem) => {
      if (this.meta.has(item.id)) out.push(item);
      item.children.forEach(visit);
    };
    if (req.include) req.include.forEach(visit);
    else this.ctrl.items.forEach(visit);
    const excluded = new Set((req.exclude ?? []).map((i) => i.id));
    return out.filter((i) => !excluded.has(i.id));
  }

  private async run(req: vscode.TestRunRequest, token: vscode.CancellationToken): Promise<void> {
    const run = this.ctrl.createTestRun(req);
    const tests = this.requested(req);

    // Group by project root: one CLI invocation per project runs the whole
    // selection there, rather than one process per test.
    const byRoot = new Map<string, vscode.TestItem[]>();
    for (const t of tests) {
      const m = this.meta.get(t.id)!;
      byRoot.set(m.cwd, [...(byRoot.get(m.cwd) ?? []), t]);
      run.enqueued(t);
    }

    for (const [root, items] of byRoot) {
      if (token.isCancellationRequested) break;
      const byName = new Map(items.map((i) => [this.meta.get(i.id)!.name, i]));
      const pattern = `^(${[...byName.keys()].map(escapeRe).join("|")})$`;
      for (const i of items) run.started(i);

      let results: RunResult[];
      try {
        results = parseNdjson<RunResult>(
          await runCli(root, ["test", "-json", "-run", pattern, "."]),
        );
      } catch (err) {
        // The project didn't compile, or the CLI is missing: the failure
        // belongs on every test in the selection, not in a popup.
        const message = new vscode.TestMessage(String(err instanceof Error ? err.message : err));
        for (const i of items) run.errored(i, message);
        continue;
      }

      const seen = new Set<string>();
      for (const r of results) {
        const item = byName.get(r.name);
        if (!item) continue;
        seen.add(r.name);
        if (r.passed) {
          run.passed(item, r.elapsedMs);
          continue;
        }
        run.failed(item, this.message(r, item), r.elapsedMs);
      }
      // A test the CLI never reported on (renamed on disk, say) is stale
      // rather than passing.
      for (const [name, item] of byName) {
        if (!seen.has(name)) run.skipped(item);
      }
    }
    run.end();
  }

  /** A failure message: the assertion, then the trajectory that explains it. */
  private message(r: RunResult, item: vscode.TestItem): vscode.TestMessage {
    const f = r.failure;
    const lines: string[] = [];
    if (f) {
      const at = `t=${(f.atMs / 1000).toFixed(3)}s of virtual time`;
      lines.push(f.step > 0 ? `step ${f.step}, ${at}` : at);
      lines.push("");
      lines.push(f.detail ? `${f.detail}   (${f.reason})` : f.reason);
      for (const t of f.trace ?? []) {
        const head = [t.name, t.unit, t.desc && `— ${t.desc}`].filter(Boolean).join("  ");
        lines.push("", head);
        lines.push(t.atMs.map((ms) => `${(ms / 1000).toFixed(2)}s`.padStart(11)).join(""));
        lines.push(t.values.map((v) => v.padStart(11)).join(""));
      }
    } else {
      lines.push("failed");
    }
    const msg = new vscode.TestMessage(lines.join("\n"));
    if (item.uri && f?.line) {
      const line = Math.max(0, f.line - 1);
      msg.location = new vscode.Location(item.uri, new vscode.Position(line, 0));
    }
    return msg;
  }
}
