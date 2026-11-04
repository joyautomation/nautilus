// Making the managed naut usable from a terminal outside VS Code.
//
// "Install or Update the naut CLI" writes naut into the extension's global
// storage, which no shell has on PATH. With the user's OK (cli.ts asks, and
// lists exactly what changes), this puts it within reach of a new terminal
// without admin rights:
//
//   macOS / Linux  a symlink ~/.local/bin/naut -> the managed binary (so an
//                  update through the extension is picked up), plus, when
//                  ~/.local/bin is not already on PATH, one marked line that
//                  APPENDS it to PATH in the shell startup files (sh/bash/zsh)
//                  and a marked fish conf.d file. Appending means a naut you
//                  installed yourself (/usr/local/bin, ~/go/bin, Homebrew)
//                  keeps winning.
//   Windows        the managed bin directory is appended to the per-user PATH
//                  (HKCU\Environment), written back as REG_EXPAND_SZ so
//                  %USERPROFILE%-style entries keep expanding, then
//                  WM_SETTINGCHANGE is broadcast so new terminals see it.
//
// removeUnix / removeWindows undo exactly that, recognising our lines by
// PROFILE_MARK and our link by where it points.
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

function realOrResolved(p: string): string {
  try {
    return fs.realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

/** True when `a` and `b` name the same file once symlinks are followed:
 * ~/.local/bin/naut (our link) is the managed binary. Falls back to a plain
 * path comparison when either doesn't exist. */
export function sameFile(a: string, b: string | undefined, platform: NodeJS.Platform = process.platform): boolean {
  if (!b) return false;
  const norm = (p: string) => (platform === "win32" ? realOrResolved(p).toLowerCase() : realOrResolved(p));
  return norm(a) === norm(b);
}

/** True when `p` is `dir` itself or lies inside it (not merely shares a
 * prefix: /x/bin2 is not inside /x/bin). Purely lexical. */
export function isInside(p: string, dir: string, platform: NodeJS.Platform = process.platform): boolean {
  const m = pathMod(platform);
  let a = m.resolve(p);
  let d = m.resolve(dir);
  if (platform === "win32") {
    a = a.toLowerCase();
    d = d.toLowerCase();
  }
  return a === d || a.startsWith(d.endsWith(m.sep) ? d : d + m.sep);
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

/** A startup file that gets the PATH line. */
export interface ProfileFile {
  file: string;
  /** Created when missing (the user's own shell's file); the others are only
   * edited when they already exist. */
  create: boolean;
}

export type SymlinkPlan = {
  kind: "symlink";
  home: string;
  linkDir: string;
  link: string;
  target: string;
  /** The directory is already on PATH for this process. */
  linkDirOnPath: boolean;
  /** sh/bash/zsh startup files that get `line` when linkDir isn't on PATH. */
  profiles: ProfileFile[];
  line: string;
  /** fish conf.d file (when fish is set up), with `fishLine`. */
  fishFile?: string;
  fishLine: string;
};
export type UserPathPlan = { kind: "userPath"; dir: string };
export type PathPlan = SymlinkPlan | UserPathPlan;

/** Every sh-family startup file we may ever have edited (for removal). */
function shellFiles(home: string): string[] {
  return [".profile", ".bashrc", ".zshrc", ".zprofile", ".bash_profile"].map((f) => path.posix.join(home, f));
}

function fishFileOf(home: string): string {
  return path.posix.join(home, ".config", "fish", "conf.d", "nautilus.fish");
}

export function planPathSetup(e: PathEnv): PathPlan {
  if (e.platform === "win32") return { kind: "userPath", dir: e.managedBinDir };
  const p = path.posix;
  const linkDir = p.join(e.home, ".local", "bin");
  const shell = p.basename(e.env.SHELL ?? "");
  const f = (name: string, create = false): ProfileFile => ({ file: p.join(e.home, name), create });
  // The user's login shell gets its file created if need be; the rest are
  // edited only when they exist. macOS terminals start login shells
  // (.zprofile/.bash_profile); Linux terminals start interactive non-login
  // ones (.bashrc/.zshrc), while the desktop session reads .profile.
  const profiles =
    e.platform === "darwin"
      ? shell === "bash"
        ? [f(".bash_profile", true), f(".zprofile")]
        : [f(".zprofile", true), f(".bash_profile")]
      : shell === "zsh"
        ? [f(".zshrc", true), f(".zprofile"), f(".profile"), f(".bashrc")]
        : [f(".profile", true), f(".bashrc"), f(".zshrc")];
  const fishUsed = shell === "fish" || fs.existsSync(p.join(e.home, ".config", "fish"));
  return {
    kind: "symlink",
    home: e.home,
    linkDir,
    link: p.join(linkDir, "naut"),
    target: p.join(e.managedBinDir, "naut"),
    linkDirOnPath: onPath(linkDir, e.env.PATH, e.platform),
    profiles,
    // Appends (a naut already on PATH keeps winning) and only once, however
    // many nested shells source it. One line so removal is by line.
    line: `case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) export PATH="$PATH:$HOME/.local/bin" ;; esac  ${PROFILE_MARK}`,
    fishFile: fishUsed ? fishFileOf(e.home) : undefined,
    fishLine: `contains -- $HOME/.local/bin $PATH; or set -gx PATH $PATH $HOME/.local/bin  ${PROFILE_MARK}`,
  };
}

/** `file` with the home directory shown as ~, for messages. */
export function tildify(file: string, home: string): string {
  return file === home ? "~" : file.startsWith(home + "/") ? "~" + file.slice(home.length) : file;
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

/** Where our link points (lexically), or undefined when `link` isn't a symlink. */
function linkTarget(link: string): string | undefined {
  try {
    if (!fs.lstatSync(link).isSymbolicLink()) return undefined;
    return path.resolve(path.dirname(link), fs.readlinkSync(link));
  } catch {
    return undefined;
  }
}

function exists(p: string): boolean {
  try {
    fs.lstatSync(p);
    return true;
  } catch {
    return false;
  }
}

/** One change applyUnix would make, described for the consent prompt. */
export interface UnixChange {
  kind: "link" | "profile" | "fish";
  file: string;
  describe: string;
}

/** What applyUnix would change, without changing anything. `blocked` is set
 * when a naut we didn't make sits at the link path (then nothing is done:
 * putting ~/.local/bin on PATH would expose that one instead). */
export function unixChanges(plan: SymlinkPlan, managedBinDir: string): { changes: UnixChange[]; blocked?: string } {
  const changes: UnixChange[] = [];
  const t = (f: string) => tildify(f, plan.home);
  if (exists(plan.link)) {
    const cur = linkTarget(plan.link);
    if (!cur || !isInside(cur, managedBinDir, "linux")) {
      return { changes, blocked: `${t(plan.link)} already exists and was not made by this extension, so it was left alone` };
    }
    if (cur !== path.resolve(plan.target)) {
      changes.push({ kind: "link", file: plan.link, describe: `re-point the link ${t(plan.link)} to ${plan.target}` });
    }
  } else {
    changes.push({ kind: "link", file: plan.link, describe: `create the link ${t(plan.link)} → ${plan.target}` });
  }
  if (plan.linkDirOnPath) return { changes };
  for (const { file, create } of plan.profiles) {
    const has = fs.existsSync(file);
    if (!has && !create) continue;
    if (has && fs.readFileSync(file, "utf8").includes(PROFILE_MARK)) continue;
    changes.push({ kind: "profile", file, describe: `${has ? "add one line to" : "create"} ${t(file)} (appends ~/.local/bin to PATH)` });
  }
  if (plan.fishFile && !(fs.existsSync(plan.fishFile) && fs.readFileSync(plan.fishFile, "utf8").includes(PROFILE_MARK))) {
    changes.push({ kind: "fish", file: plan.fishFile, describe: `create ${t(plan.fishFile)} (fish: appends ~/.local/bin to PATH)` });
  }
  return { changes };
}

const UNDO_CMD = 'run "nautilus: Remove naut from PATH"';

/** macOS and Linux. Never replaces a naut it didn't put there. */
export function applyUnix(plan: SymlinkPlan, managedBinDir: string): PathResult {
  const r: PathResult = { done: [], undo: [], newTerminal: true };
  const { changes, blocked } = unixChanges(plan, managedBinDir);
  if (blocked) {
    r.skipped = blocked;
    return r;
  }
  const t = (f: string) => tildify(f, plan.home);
  const files: string[] = [];
  for (const c of changes) {
    if (c.kind === "link") {
      fs.mkdirSync(plan.linkDir, { recursive: true });
      if (exists(plan.link)) fs.rmSync(plan.link);
      fs.symlinkSync(plan.target, plan.link);
    } else if (c.kind === "profile") {
      const text = fs.existsSync(c.file) ? fs.readFileSync(c.file, "utf8") : "";
      fs.appendFileSync(c.file, `${text && !text.endsWith("\n") ? "\n" : ""}${plan.line}\n`);
      files.push(t(c.file));
    } else {
      fs.mkdirSync(path.dirname(c.file), { recursive: true });
      fs.writeFileSync(c.file, `${plan.fishLine}\n`);
      files.push(t(c.file));
    }
  }
  if (exists(plan.link)) r.done.push(`linked ${t(plan.link)} to the extension's naut`);
  if (files.length) r.done.push(`added ~/.local/bin to the end of PATH in ${files.join(", ")}`);
  if (r.done.length) r.undo.push(UNDO_CMD);
  return r;
}

/** Undo applyUnix: our link (one pointing into managedBinDir, even when the
 * binary is gone), our marked lines, our fish file. Nothing else. */
export function removeUnix(e: PathEnv): string[] {
  const done: string[] = [];
  const t = (f: string) => tildify(f, e.home);
  const link = path.posix.join(e.home, ".local", "bin", "naut");
  const cur = linkTarget(link);
  if (cur && isInside(cur, e.managedBinDir, "linux")) {
    fs.rmSync(link);
    done.push(`removed the link ${t(link)}`);
  }
  for (const f of shellFiles(e.home)) {
    if (!fs.existsSync(f)) continue;
    const text = fs.readFileSync(f, "utf8");
    if (!text.includes(PROFILE_MARK)) continue;
    fs.writeFileSync(f, text.split("\n").filter((l) => !l.includes(PROFILE_MARK)).join("\n"));
    done.push(`removed the nautilus line from ${t(f)}`);
  }
  const fish = fishFileOf(e.home);
  if (fs.existsSync(fish) && fs.readFileSync(fish, "utf8").includes(PROFILE_MARK)) {
    fs.rmSync(fish);
    done.push(`removed ${t(fish)}`);
  }
  return done;
}

/** True when `link` is our link but what it points at is gone (the managed
 * copy was deleted). */
export function danglingOwnLink(home: string, managedBinDir: string): string | undefined {
  const link = path.posix.join(home, ".local", "bin", "naut");
  const cur = linkTarget(link);
  return cur && isInside(cur, managedBinDir, "linux") && !fs.existsSync(cur) ? link : undefined;
}

// ── Windows ────────────────────────────────────────────────────────────────

/** The new user PATH with `dir` appended, or undefined when already there. */
export function withDirOnUserPath(current: string, dir: string): string | undefined {
  if (onPath(dir, current, "win32")) return undefined;
  const base = current.replace(/;+$/, "");
  return base ? `${base};${dir}` : dir;
}

/** The user PATH with every `dir` entry removed, or undefined when it has none. */
export function withoutDirOnUserPath(current: string, dir: string): string | undefined {
  const entries = current.split(";");
  const kept = entries.filter((e) => !(e && onPath(dir, e, "win32")));
  return kept.length === entries.length ? undefined : kept.join(";");
}

export type RunPowerShell = (script: string, env: Record<string, string>) => Promise<string>;

// Read the raw value (%USERPROFILE%\… stays unexpanded) and write it back as
// REG_EXPAND_SZ. [Environment]::SetEnvironmentVariable would write REG_SZ,
// after which Windows stops expanding those entries (the rustup/Chocolatey
// bug). Then broadcast WM_SETTINGCHANGE so Explorer, and the terminals it
// starts, pick up the new PATH without a sign-out; if the broadcast fails, new
// sign-ins still do.
//
// The value crosses the process boundary as base64 of its UTF-16LE bytes, both
// ways: Windows PowerShell writes redirected stdout in the OEM code page, so a
// plain read would turn C:\Users\José\… into U+FFFD and the write would store
// that. Any failure to write exits non-zero with the reason on stderr, and the
// write is read back before it counts.
export const READ_USER_PATH = [
  "$ErrorActionPreference = 'Stop'",
  "$v = [string](Get-Item -Path 'HKCU:\\Environment').GetValue('Path', '', 'DoNotExpandEnvironmentNames')",
  "[Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($v))",
].join("\n");
export const WRITE_USER_PATH = [
  "$ErrorActionPreference = 'Stop'",
  "try {",
  "  $p = [Text.Encoding]::Unicode.GetString([Convert]::FromBase64String($env:NAUT_NEW_PATH_B64))",
  "  $k = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)",
  "  if (-not $k) { throw 'HKCU\\Environment cannot be opened for writing' }",
  "  $k.SetValue('Path', $p, [Microsoft.Win32.RegistryValueKind]::ExpandString)",
  "  $back = $k.GetValue('Path', '', 'DoNotExpandEnvironmentNames')",
  "  $kind = $k.GetValueKind('Path')",
  "  $k.Close()",
  "  if ($back -ne $p -or $kind -ne [Microsoft.Win32.RegistryValueKind]::ExpandString) { throw 'the new value did not stick' }",
  "} catch {",
  `  [Console]::Error.WriteLine("couldn't write the user PATH: $($_.Exception.Message)")`,
  "  exit 1",
  "}",
  "try {",
  `  Add-Type -Namespace NautPath -Name Native -MemberDefinition '[DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint msg, UIntPtr wParam, string lParam, uint flags, uint timeout, out UIntPtr result);'`,
  "  $r = [UIntPtr]::Zero",
  "  [void][NautPath.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$r)",
  "} catch {}",
  "exit 0",
].join("\n");

/** The user PATH, raw (unexpanded), exactly as stored. */
export async function readUserPath(run: RunPowerShell): Promise<string> {
  const b64 = (await run(READ_USER_PATH, {})).trim();
  if (!/^[A-Za-z0-9+/]*={0,2}$/.test(b64)) throw new Error(`unexpected answer reading the user PATH: ${b64.slice(0, 80)}`);
  return Buffer.from(b64, "base64").toString("utf16le");
}

/** Store `value` as the user PATH (REG_EXPAND_SZ). Rejects when it didn't stick. */
export async function writeUserPath(value: string, run: RunPowerShell): Promise<void> {
  await run(WRITE_USER_PATH, { NAUT_NEW_PATH_B64: Buffer.from(value, "utf16le").toString("base64") });
}

/** What applyWindows would change: the new user PATH, or undefined when the
 * directory is already on it. Reads only. */
export async function windowsChange(plan: UserPathPlan, run: RunPowerShell): Promise<string | undefined> {
  return withDirOnUserPath(await readUserPath(run), plan.dir);
}

/** Windows: per-user PATH only (no admin). Rejects, claiming nothing, when
 * the write fails. */
export async function applyWindows(plan: UserPathPlan, run: RunPowerShell): Promise<PathResult> {
  const r: PathResult = { done: [], undo: [], newTerminal: true };
  const next = await windowsChange(plan, run);
  if (next === undefined) {
    r.done.push(`${plan.dir} is already on your user PATH`);
    return r;
  }
  await writeUserPath(next, run);
  r.done.push(`added ${plan.dir} to the end of your user PATH`);
  r.undo.push(UNDO_CMD);
  return r;
}

/** Undo applyWindows: drop the managed directory from the user PATH. */
export async function removeWindows(dir: string, run: RunPowerShell): Promise<string[]> {
  const next = withoutDirOnUserPath(await readUserPath(run), dir);
  if (next === undefined) return [];
  await writeUserPath(next, run);
  return [`removed ${dir} from your user PATH`];
}

/** The sentence(s) for the notification. */
export function describeResult(r: PathResult): string {
  if (r.skipped && !r.done.length) return `${r.skipped}.`;
  const parts = [...r.done];
  if (r.skipped) parts.push(r.skipped);
  const tail = r.newTerminal && r.undo.length ? " Open a new terminal window outside VS Code to use it there." : "";
  return `${parts.join("; ")}.${tail}`;
}
