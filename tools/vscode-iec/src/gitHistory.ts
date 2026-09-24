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
  /** The file's path, relative to the repository root, AT THIS COMMIT —
   * `--follow` walks through renames, so it can differ from today's. */
  path: string;
};

/** Field and record separators for the log format: US (0x1f) between
 * fields, RS (0x1e) between commits, so a subject may contain anything. */
const FS = "\x1f";
const RS = "\x1e";
export const LOG_FORMAT = `%h${FS}%H${FS}%ad${FS}%an${FS}%s${RS}`;

/** Parse `git log --name-only --format=LOG_FORMAT` output. Each record is
 * the five fields, RS, then the path the file had at that commit on its
 * own line (that is what `--name-only` adds, and with `--follow` it is the
 * pre-rename path for older commits). Tolerates blank lines; a malformed
 * record is dropped, not thrown. */
export function parseGitLog(stdout: string): GitCommit[] {
  const out: GitCommit[] = [];
  const chunks = stdout.split(RS);
  // chunks[k] = "<path of record k-1>\n<fields of record k>" (k > 0);
  // chunks[0] is record 0's fields; the last chunk is only a path.
  let pending: Omit<GitCommit, "path"> | undefined;
  for (const chunk of chunks) {
    const lines = chunk.split("\n");
    const fieldsAt = lines.findIndex((l) => l.includes(FS));
    const pathLines = (fieldsAt < 0 ? lines : lines.slice(0, fieldsAt)).map((l) => l.trim()).filter(Boolean);
    if (pending) {
      out.push({ ...pending, path: pathLines[pathLines.length - 1] ?? "" });
      pending = undefined;
    }
    if (fieldsAt < 0) continue;
    const f = lines[fieldsAt].split(FS);
    if (f.length < 5) continue;
    pending = { short: f[0], sha: f[1], date: f[2], author: f[3], subject: f.slice(4).join(FS) };
  }
  if (pending) out.push({ ...pending, path: "" });
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
      ["log", "--follow", "--name-only", "--date=short", `--format=${LOG_FORMAT}`, "--", `./${base}`],
      { cwd: dir, maxBuffer: MAX_BUFFER },
      (err, stdout) => resolve(err ? [] : parseGitLog(stdout))
    );
  });
}

/** The file's content at `ref` (a sha, `HEAD`, a tag, `HEAD~3`…), or
 * undefined if the file does not exist at that revision. `repoPath` is
 * the file's repository-relative path at that revision (a `GitCommit`'s
 * `path`); without it the file's current name is used, which is wrong
 * across a rename. */
export function gitShow(fsPath: string, ref: string, repoPath?: string): Promise<string | undefined> {
  const dir = path.dirname(fsPath);
  const base = path.basename(fsPath);
  // A bare path is relative to the repo root; "./" makes it relative to cwd.
  const spec = repoPath ? `${ref}:${repoPath}` : `${ref}:./${base}`;
  return new Promise((resolve) => {
    execFile(
      "git",
      ["show", spec],
      { cwd: dir, maxBuffer: MAX_BUFFER },
      (err, stdout) => resolve(err ? undefined : stdout)
    );
  });
}
