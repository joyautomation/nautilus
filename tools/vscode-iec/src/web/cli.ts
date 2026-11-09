// cli.ts for the web build: in the playground, `naut` is the engine running
// beside the editor, always there and always the right version. Installing
// it, putting it on PATH and terminals are for a real machine, and say so.
import * as vscode from "vscode";
import type { ExecFileOptionsWithStringEncoding } from "./child_process";
import { engine } from "./engine";

export const CLI_TIMEOUT_MS = 15_000;
export const PATH_DECLINED_KEY = "nautilus.cli.pathDeclined";
export const TERMINAL_PATH_OFF_KEY = "nautilus.cli.terminalPathOff";

const ON_A_REAL_MACHINE =
  "This runs on your own machine: the playground is nautilus in your browser. Install VS Code, the nautilus extension and naut to use it.";

export function cliExecOptions(o: { timeoutMs?: number; maxBuffer?: number; cwd?: string } = {}): ExecFileOptionsWithStringEncoding {
  return { cwd: o.cwd, timeout: o.timeoutMs ?? CLI_TIMEOUT_MS, maxBuffer: o.maxBuffer, encoding: "utf8" };
}
export function initCli(_context: vscode.ExtensionContext): void {}
export function isManagedCli(_cmd: string): boolean {
  return false;
}
export function syncTerminalPath(): boolean {
  return false;
}
export function setPowerShellForTest(): void {}
export async function offerPathSetup(): Promise<string> {
  return "";
}
async function realMachineOnly(): Promise<void> {
  const pick = await vscode.window.showInformationMessage(ON_A_REAL_MACHINE, "How to install");
  if (pick) void vscode.env.openExternal(vscode.Uri.parse("https://nautilus.joyautomation.com/tutorial/"));
}
export const addCliToPathCommand = realMachineOnly;
export const removeCliFromPathCommand = realMachineOnly;
export const installCliCommand = realMachineOnly;
export function minCliVersionNeeded(): string {
  return "";
}
export function managedCliPath(): string | undefined {
  return undefined;
}
export function resolveCliNow(): { found: boolean; command: string; source?: string } {
  return { found: engine.available(), command: "naut", source: "playground" };
}
export function cliCommand(): string {
  return "naut";
}
export function inFlatpak(): boolean {
  return false;
}
export function cliMissingMessage(_cli: string): string {
  return "nautilus: the playground engine is not running. Reload the page.";
}
export function isMissing(err: unknown): boolean {
  return (err as { code?: unknown })?.code === "ENOENT";
}
export function showCliMissing(_cli: string, why?: string): void {
  void vscode.window.showErrorMessage(why ?? cliMissingMessage(""));
}
export async function cliVersion(): Promise<string | undefined> {
  try {
    const r = await engine.call<{ stdout: string }>("exec", ["version"], "", "/project");
    return r.stdout.trim();
  } catch {
    return undefined;
  }
}
export async function checkCliVersion(): Promise<void> {}
export async function showCliInfo(): Promise<void> {
  void vscode.window.showInformationMessage(`${(await cliVersion()) ?? "naut"}: the nautilus compiler and runtime, running in this browser tab.`);
}
