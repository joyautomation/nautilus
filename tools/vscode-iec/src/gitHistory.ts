// A file's git history, for the diagram diffs: every commit that touched
// the file (`git log --follow`) and the file's content at any revision
// (`git show <ref>:./file`). No VS Code imports — the revision picker
// (revisionPick.ts) is the UI over this, and the parser is unit-tested on
// its own.

import { execFile } from "child_process";
import * as path from "path";

export type GitCommit = {
  /** Abbreviated sha, as `git log` prints it (`%h`). */
  short: string;
  /** Full sha (`%H`). */
  sha: string;
  /** Author date, `YYYY-MM-DD`. */
  date: string;
  author: string;
  subject: string;
};

/** Field and record separators for the log format: US (0x1f) between
 * fields, RS (0x1e) between commits, so a subject may contain anything. */
const FS = "\x1f";
const RS = "\x1e";
export const LOG_FORMAT = `%h${FS}%H${FS}%ad${FS}%an${FS}%s${RS}`;

/** Parse `git log --format=LOG_FORMAT` output. Tolerates a trailing
 * newline and blank records; a malformed record is dropped, not thrown. */
export function parseGitLog(stdout: string): GitCommit[] {
  const out: GitCommit[] = [];
  for (const rec of stdout.split(RS)) {
    const line = rec.replace(/^\n+/, "");
    if (!line.trim()) continue;
    const f = line.split(FS);
    if (f.length < 5) continue;
    out.push({ short: f[0], sha: f[1], date: f[2], author: f[3], subject: f.slice(4).join(FS) });
  }
  return out;
}

const MAX_BUFFER = 16 * 1024 * 1024;

/** Every commit that touched the file, newest first, following renames.
 * Empty when the file is untracked or the directory is not a repository. */
export function gitLog(fsPath: string): Promise<GitCommit[]> {
  const dir = path.dirname(fsPath);
  const base = path.basename(fsPath);
  return new Promise((resolve) => {
    execFile(
      "git",
      ["log", "--follow", "--date=short", `--format=${LOG_FORMAT}`, "--", `./${base}`],
      { cwd: dir, maxBuffer: MAX_BUFFER },
      (err, stdout) => resolve(err ? [] : parseGitLog(stdout))
    );
  });
}

/** The file's content at `ref` (a sha, `HEAD`, a tag, `HEAD~3`…), or
 * undefined if the file does not exist at that revision. */
export function gitShow(fsPath: string, ref: string): Promise<string | undefined> {
  const dir = path.dirname(fsPath);
  const base = path.basename(fsPath);
  return new Promise((resolve) => {
    // "./" makes the path relative to cwd rather than the repo root.
    execFile(
      "git",
      ["show", `${ref}:./${base}`],
      { cwd: dir, maxBuffer: MAX_BUFFER },
      (err, stdout) => resolve(err ? undefined : stdout)
    );
  });
}
