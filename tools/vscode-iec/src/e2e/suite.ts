// Runs inside the extension host that run.ts launches; NAUTILUS_E2E_EXPECT
// says which world it's in. No test framework: VS Code only needs run() to
// resolve or reject.

import * as assert from "node:assert/strict";
import { spawn } from "node:child_process";
import * as os from "node:os";
import * as path from "node:path";
import * as vscode from "vscode";
import { cliVersion, managedCliPath, resolveCliNow } from "../cli";
import { nautCompose } from "../compose";
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

/** Online edit with a ladder library: `naut compose` puts the library's
 * block in the prelude, transpiled, and "Download Program to Controller"
 * sends that (with the unsaved buffer) to a real controller, which accepts
 * it. Before `naut compose`, the prelude held .st libraries only and the
 * controller refused the program ("unknown type PumpSeq"). */
async function downloadWithLadderLibrary(cli: string, folder: vscode.Uri): Promise<void> {
  const proj = vscode.Uri.joinPath(folder, "compose");
  const main = vscode.Uri.joinPath(proj, "main.ld");
  const composed = await step("naut compose main.ld", 30_000, () => nautCompose(main));
  assert.ok("ok" in composed, `naut compose answered: ${JSON.stringify(composed)}`);
  assert.deepEqual(composed.ok.libraries, ["lib/rungs.ld"]);
  assert.match(composed.ok.prelude, /FUNCTION_BLOCK PumpSeq/);
  assert.doesNotMatch(composed.ok.prelude, /RUNG/, "the ladder library arrives transpiled");
  assert.deepEqual(composed.ok.programs.map((p) => [p.file, p.pou, p.language]), [["main.ld", "Main", "ld"]]);

  const url = `http://127.0.0.1:${process.env.NAUTILUS_E2E_PORT}`;
  const controller = spawn(cli, ["run", proj.fsPath], { stdio: "ignore" });
  try {
    const cfg = vscode.workspace.getConfiguration("nautilus");
    await cfg.update("runtimeUrl", url, vscode.ConfigurationTarget.Global);
    await cfg.update("confirmControllerWrites", false, vscode.ConfigurationTarget.Global);
    const boot = await step("controller up", 60_000, async () => {
      for (;;) {
        try {
          const res = await fetch(url + "/api/program");
          if (res.ok) return (await res.json()) as { hash: string; dirty: boolean };
        } catch {
          /* not listening yet */
        }
        await new Promise((r) => setTimeout(r, 250));
      }
    });
    assert.equal(boot.dirty, false);

    // An unsaved edit: the download must carry the buffer, not the disk.
    const doc = await vscode.workspace.openTextDocument(main);
    const editor = await vscode.window.showTextDocument(doc);
    await editor.edit((e) => e.insert(new vscode.Position(1, 0), "  (* edited online *)\n"));
    await step("download", 30_000, () => vscode.commands.executeCommand("nautilus.program.download"));
    const after = (await (await fetch(url + "/api/program")).json()) as { source: string; dirty: boolean; language: string };
    assert.equal(after.dirty, true, "the controller took the download");
    assert.equal(after.language, "ld");
    assert.equal(after.source, composed.ok.prelude + doc.getText(), "it runs the composed prelude + the edited buffer");
  } finally {
    controller.kill();
    await vscode.commands.executeCommand("workbench.action.revertAndCloseActiveEditor");
  }
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
    if (expect === "found") await downloadWithLadderLibrary(cli.command, folder.uri);
  } else if (expect === "missing") {
    assert.equal(cli.found, false, `nothing to find, but resolved ${cli.command}`);
    assert.ok(cli.searched.includes(path.join(os.homedir(), "go", "bin")), "searched ~/go/bin");

    // Give a language server that shouldn't exist time to prove it doesn't.
    await new Promise((r) => setTimeout(r, 3_000));
    assert.deepEqual(nautilusDiagnostics(uri), []);

    const g = await step("fbd graph without a CLI", 30_000, () => fbdGraph(FBD));
    assert.ok("error" in g && /nautilus\.cliPath/.test(g.error), `the diagram editor says how to fix it: ${JSON.stringify(g)}`);
    const c = await step("naut compose without a CLI", 30_000, () =>
      nautCompose(vscode.Uri.joinPath(folder.uri, "compose", "main.ld"))
    );
    assert.ok("error" in c && /nautilus\.cliPath/.test(c.error), `online edits say how to fix it: ${JSON.stringify(c)}`);
  } else {
    throw new Error(`NAUTILUS_E2E_EXPECT must be found, missing or managed, got ${expect}`);
  }
  console.log(`[e2e +${Date.now() - t0}ms] ${expect}: all checks passed`);
}
