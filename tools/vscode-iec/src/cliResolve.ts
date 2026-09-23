// Finding the nautilus CLI without trusting VS Code's PATH.
//
// A terminal and the editor disagree about PATH more often than not: VS Code
// launched from a desktop menu or dock never reads ~/.bashrc or ~/.zshrc, so
// the `export PATH=$PATH:~/go/bin` that makes `nautilus version` answer in a
// shell is invisible to the extension. `go install` is the install most Go
// developers reach for, and it lands exactly there. So a bare command name is
// looked up on PATH first and then in the places the documented installs put
// the binary; only an explicit path is taken as given.
//
// Pure (no vscode import) so node:test covers it; the filesystem and the
// environment come in through ResolveEnv.

import * as path from "node:path";

export interface ResolveEnv {
  env: Record<string, string | undefined>;
  platform: NodeJS.Platform;
  home: string;
  /** True when `p` is a regular file the extension may execute. */
  isExecutable(p: string): boolean;
}

export interface Resolved {
  /** What to hand execFile / the language client. */
  command: string;
  /** False when a bare name was searched for and not found anywhere. */
  found: boolean;
  /** The directories a bare name was searched in, in order (for the error). */
  searched: string[];
}

/** Where the documented installs put the binary, beyond PATH: `go install`
 * ($GOBIN, else the first $GOPATH entry's bin, else ~/go/bin), the README's
 * release-archive installs (/usr/local/bin; %LOCALAPPDATA%\nautilus), and the
 * usual per-user and Homebrew bins. */
export function fallbackDirs(e: ResolveEnv): string[] {
  const p = e.platform === "win32" ? path.win32 : path.posix;
  const dirs: string[] = [];
  if (e.env.GOBIN) dirs.push(e.env.GOBIN);
  for (const gp of (e.env.GOPATH ?? "").split(p.delimiter)) {
    if (gp) dirs.push(p.join(gp, "bin"));
  }
  dirs.push(p.join(e.home, "go", "bin"));
  if (e.platform === "win32") {
    if (e.env.LOCALAPPDATA) dirs.push(p.join(e.env.LOCALAPPDATA, "nautilus"));
  } else {
    dirs.push(p.join(e.home, ".local", "bin"), "/usr/local/bin", "/opt/homebrew/bin");
  }
  return dirs;
}

function expandHome(cmd: string, e: ResolveEnv): string {
  if (cmd === "~") return e.home;
  if (cmd.startsWith("~/") || cmd.startsWith("~\\")) return e.home + cmd.slice(1);
  return cmd;
}

export function resolveCli(configured: string | undefined, e: ResolveEnv): Resolved {
  const p = e.platform === "win32" ? path.win32 : path.posix;
  const cmd = expandHome((configured ?? "").trim() || "nautilus", e);

  // An explicit path is the user's word: use it, and let a bad one fail loudly.
  if (cmd.includes("/") || (e.platform === "win32" && cmd.includes("\\"))) {
    return { command: cmd, found: true, searched: [] };
  }

  const names =
    e.platform === "win32" && !p.extname(cmd)
      ? [cmd + ".exe", cmd + ".cmd", cmd + ".bat"]
      : [cmd];
  const pathDirs = (e.env.PATH ?? e.env.Path ?? "").split(p.delimiter).filter(Boolean);
  const searched: string[] = [];
  for (const dir of [...pathDirs, ...fallbackDirs(e)]) {
    if (searched.includes(dir)) continue;
    searched.push(dir);
    for (const name of names) {
      const full = p.join(dir, name);
      if (e.isExecutable(full)) return { command: full, found: true, searched };
    }
  }
  return { command: cmd, found: false, searched };
}
