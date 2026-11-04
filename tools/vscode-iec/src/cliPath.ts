// Making the managed naut usable from a terminal.
//
// "Install or Update the naut CLI" writes naut into the extension's global
// storage, which no shell has on PATH. This puts it within reach of a new
// terminal without admin rights:
//
//   macOS / Linux  a symlink ~/.local/bin/naut -> the managed binary (so an
//                  update through the extension is picked up), plus, when
//                  ~/.local/bin is not already on PATH, one marked line in
//                  the shell profile files that adds it.
//   Windows        the managed bin directory is added to the per-user PATH
//                  (HKCU\Environment), with no admin rights.
//
// The integrated terminal is handled separately (environmentVariableCollection
// in cli.ts) so it works without reopening anything.
//
// No vscode import; the home directory and the PowerShell call are injected so
// node:test covers every platform's logic on any host.

import * as fs from "node:fs";
import * as path from "node:path";

export const PROFILE_MARK = "# added by the nautilus VS Code extension";

export interface PathEnv {
  platform: NodeJS.Platform;
  home: string;
  env: Record<string, string | undefined>;
  /** The extension-managed bin directory holding naut(.exe). */
  managedBinDir: string;
}

function pathMod(platform: NodeJS.Platform) {
  return platform === "win32" ? path.win32 : path.posix;
}

/** True when `dir` is one of the entries of a PATH string. */
export function onPath(dir: string, pathVar: string | undefined, platform: NodeJS.Platform): boolean {
  const p = pathMod(platform);
  const norm = (d: string) => {
    let r = p.normalize(d).replace(/[\\/]+$/, "");
    if (platform === "win32") r = r.toLowerCase();
    return r;
  };
  const want = norm(dir);
  return (pathVar ?? "").split(p.delimiter).some((e) => e && norm(e) === want);
}

export type SymlinkPlan = {
  kind: "symlink";
  linkDir: string;
  link: string;
  target: string;
  /** The directory is already on PATH for this process. */
  linkDirOnPath: boolean;
  /** Profile files that get the PATH line when it isn't; the first is created if missing. */
  profiles: string[];
  line: string;
};
export type UserPathPlan = { kind: "userPath"; dir: string };
export type PathPlan = SymlinkPlan | UserPathPlan;

export function planPathSetup(e: PathEnv): PathPlan {
  const p = pathMod(e.platform);
  if (e.platform === "win32") return { kind: "userPath", dir: e.managedBinDir };
  const linkDir = p.join(e.home, ".local", "bin");
  const profiles =
    e.platform === "darwin"
      ? [p.join(e.home, ".zprofile"), p.join(e.home, ".bash_profile")]
      : [p.join(e.home, ".profile"), p.join(e.home, ".bashrc")];
  return {
    kind: "symlink",
    linkDir,
    link: p.join(linkDir, "naut"),
    target: p.join(e.managedBinDir, "naut"),
    linkDirOnPath: onPath(linkDir, e.env.PATH, e.platform),
    profiles,
    line: `export PATH="$HOME/.local/bin:$PATH"  ${PROFILE_MARK}`,
  };
}

export interface PathResult {
  /** What was done, one line each, for the log and the notification. */
  done: string[];
  /** What the user can do to reverse it. */
  undo: string[];
  /** Why nothing (or only part) was done, if so. */
  skipped?: string;
  /** Takes a new terminal window (or login) to show up outside VS Code. */
  newTerminal: boolean;
}

function linkTarget(plan: SymlinkPlan): string | undefined {
  try {
    if (!fs.lstatSync(plan.link).isSymbolicLink()) return undefined;
    return path.resolve(plan.linkDir, fs.readlinkSync(plan.link));
  } catch {
    return undefined;
  }
}

/** macOS and Linux. Never replaces a naut it didn't put there. */
export function applyUnix(plan: SymlinkPlan, managedBinDir: string): PathResult {
  const r: PathResult = { done: [], undo: [], newTerminal: true };
  fs.mkdirSync(plan.linkDir, { recursive: true });

  let exists = true;
  try {
    fs.lstatSync(plan.link);
  } catch {
    exists = false;
  }
  if (exists) {
    const cur = linkTarget(plan);
    if (!cur || !cur.startsWith(path.resolve(managedBinDir))) {
      r.skipped = `${plan.link} already exists and was not made by this extension, so it was left alone`;
      return r;
    }
    if (cur !== path.resolve(plan.target)) {
      fs.rmSync(plan.link);
      fs.symlinkSync(plan.target, plan.link);
    }
  } else {
    fs.symlinkSync(plan.target, plan.link);
  }
  r.done.push(`linked ${plan.link} to the extension's naut`);
  r.undo.push(`rm ${plan.link}`);

  if (plan.linkDirOnPath) return r;
  for (const f of plan.profiles) {
    // Edit existing profiles; create only the primary one (a fresh Mac has no ~/.zprofile).
    const has = fs.existsSync(f);
    if (!has && f !== plan.profiles[0]) continue;
    const text = has ? fs.readFileSync(f, "utf8") : "";
    if (text.includes(PROFILE_MARK)) continue;
    fs.appendFileSync(f, `${text && !text.endsWith("\n") ? "\n" : ""}${plan.line}\n`);
    r.done.push(`added ~/.local/bin to PATH in ${path.basename(f)}`);
    r.undo.push(`delete the line ending "${PROFILE_MARK}" from ${f}`);
  }
  return r;
}

/** The new user PATH with `dir` appended, or undefined when already there. */
export function withDirOnUserPath(current: string, dir: string): string | undefined {
  if (onPath(dir, current, "win32")) return undefined;
  const base = current.replace(/;+$/, "");
  return base ? `${base};${dir}` : dir;
}

export type RunPowerShell = (script: string, env: Record<string, string>) => Promise<string>;

// Read the raw value so %VARS% entries survive; SetEnvironmentVariable also
// broadcasts the change so a new terminal sees it without signing out.
const READ_USER_PATH = "(Get-Item -Path 'HKCU:\\Environment').GetValue('Path', '', 'DoNotExpandEnvironmentNames')";
const WRITE_USER_PATH = "[Environment]::SetEnvironmentVariable('Path', $env:NAUT_NEW_PATH, 'User')";

/** Windows: per-user PATH only (no admin). */
export async function applyWindows(plan: UserPathPlan, run: RunPowerShell): Promise<PathResult> {
  const r: PathResult = { done: [], undo: [], newTerminal: true };
  const current = (await run(READ_USER_PATH, {})).trim();
  const next = withDirOnUserPath(current, plan.dir);
  if (next === undefined) {
    r.done.push(`${plan.dir} is already on your user PATH`);
    return r;
  }
  await run(WRITE_USER_PATH, { NAUT_NEW_PATH: next });
  r.done.push(`added ${plan.dir} to your user PATH`);
  r.undo.push(`Start > "Edit environment variables for your account" > Path: remove ${plan.dir}`);
  return r;
}

/** The sentence(s) for the notification. */
export function describeResult(r: PathResult): string {
  if (r.skipped && !r.done.length) return `${r.skipped}.`;
  const parts = [...r.done];
  if (r.skipped) parts.push(r.skipped);
  const tail = r.newTerminal ? " Open a new terminal window to use it; terminals inside VS Code already do." : "";
  return `${parts.join("; ")}.${tail}`;
}
