// Runs inside the extension host that run.ts launches; NAUTILUS_E2E_EXPECT
// says which world it's in. No test framework: VS Code only needs run() to
// resolve or reject.

import * as assert from "node:assert/strict";
import * as os from "node:os";
import * as path from "node:path";
import * as vscode from "vscode";
import { cliVersion, managedCliPath, resolveCliNow } from "../cli";
import { fbdGraph } from "../fbdPreview";

const FBD = "PROGRAM Main\nVAR\n  a : BOOL;\n  b : BOOL;\nEND_VAR\nb := a;\nEND_PROGRAM\n";

async function waitFor<T>(what: string, ms: number, probe: () => T | undefined): Promise<T> {
  const until = Date.now() + ms;
  for (;;) {
    const v = probe();
    if (v !== undefined) return v;
    if (Date.now() > until) throw new Error(`timed out after ${ms} ms waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 250));
  }
}

const t0 = Date.now();

/** Run one step under a deadline, logging it, so a hang fails CI with the
 * step's name instead of sitting until the job timeout. */
async function step<T>(name: string, ms: number, work: () => Thenable<T> | Promise<T>): Promise<T> {
  console.log(`[e2e +${Date.now() - t0}ms] ${name}`);
  let timer: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      Promise.resolve(work()),
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error(`step "${name}" hung for ${ms} ms`)), ms);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

/** Windows paths compare case-insensitively (VS Code hands out "c:\\"
 * for global storage where the environment says "C:\\"). */
function norm(p: string | undefined): string | undefined {
  return p && process.platform === "win32" ? p.toLowerCase() : p;
}

function nautilusDiagnostics(uri: vscode.Uri): vscode.Diagnostic[] {
  return vscode.languages.getDiagnostics(uri).filter((d) => d.source?.startsWith("nautilus"));
}

export async function run(): Promise<void> {
  const expect = process.env.NAUTILUS_E2E_EXPECT;
  console.log(`[e2e] suite started: expect=${expect} home=${os.homedir()} platform=${process.platform}`);
  const folder = vscode.workspace.workspaceFolders?.[0];
  assert.ok(folder, "the fixture workspace is open");
  const uri = vscode.Uri.joinPath(folder.uri, "broken.st");
  const doc = await step("open broken.st", 30_000, () => vscode.workspace.openTextDocument(uri));
  await step("show broken.st", 30_000, () => vscode.window.showTextDocument(doc));

  const ext = vscode.extensions.getExtension("joyauto.vscode-iec");
  assert.ok(ext, "the extension under test is loaded");
  await step("activate the extension", 60_000, () => ext.activate());

  const cli = resolveCliNow();
  console.log(`[e2e] resolved ${JSON.stringify(cli)}`);
  const commands = await step("list commands", 30_000, () => vscode.commands.getCommands(true));
  assert.ok(commands.includes("nautilus.restartLanguageServer"), "commands register with or without the CLI");
  assert.ok(commands.includes("nautilus.installCli"), "the one-click install is there with or without the CLI");
  const exe = process.platform === "win32" ? "naut.exe" : "naut";
  assert.equal(norm(managedCliPath()), norm(path.join(process.env.NAUTILUS_E2E_MANAGED_BIN ?? "", exe)), "the managed install lives in global storage");

  if (expect === "found" || expect === "managed") {
    const want =
      expect === "found" ? path.join(os.homedir(), "go", "bin", exe) : path.join(process.env.NAUTILUS_E2E_MANAGED_BIN ?? "", exe);
    assert.deepEqual([norm(cli.command), cli.found], [norm(want), true], `resolved the ${expect} CLI at ${want}`);
    const version = await step("naut version", 30_000, () => cliVersion(cli.command));
    assert.ok(version, "naut version answered");

    console.log(`[e2e +${Date.now() - t0}ms] wait for diagnostics`);
    const diags = await waitFor("a nautilus-st diagnostic on broken.st", 30_000, () => {
      const d = nautilusDiagnostics(uri);
      return d.length ? d : undefined;
    });
    assert.equal(diags[0].source, "nautilus-st");
    assert.equal(diags[0].range.start.line, 4);

    const g = await step("fbd graph via the CLI", 30_000, () => fbdGraph(FBD));
    assert.ok(!("error" in g) || !/nautilus\.cliPath/.test(g.error), `the diagram path reached the CLI: ${JSON.stringify(g)}`);
  } else if (expect === "missing") {
    assert.equal(cli.found, false, `nothing to find, but resolved ${cli.command}`);
    assert.ok(cli.searched.includes(path.join(os.homedir(), "go", "bin")), "searched ~/go/bin");

    // Give a language server that shouldn't exist time to prove it doesn't.
    await new Promise((r) => setTimeout(r, 3_000));
    assert.deepEqual(nautilusDiagnostics(uri), []);

    const g = await step("fbd graph without a CLI", 30_000, () => fbdGraph(FBD));
    assert.ok("error" in g && /nautilus\.cliPath/.test(g.error), `the diagram editor says how to fix it: ${JSON.stringify(g)}`);
  } else {
    throw new Error(`NAUTILUS_E2E_EXPECT must be found, missing or managed, got ${expect}`);
  }
  console.log(`[e2e +${Date.now() - t0}ms] ${expect}: all checks passed`);
}
