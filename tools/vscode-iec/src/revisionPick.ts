// The "between revisions" picker the three diagram diffs share: two quick
// picks over the file's git history (older side, then newer side), where
// the newer side may be the working tree. Returns the two sources; the
// preview freezes one or both and overlays them exactly as the HEAD and
// controller diffs do.

import * as vscode from "vscode";
import * as path from "path";
import { gitLog, gitShow, type GitCommit } from "./gitHistory";

export type RevisionSide = {
  /** Source text at that revision. */
  src: string;
  /** Short label for the diff title: the abbreviated sha, or "working tree". */
  label: string;
};

export type RevisionPair = {
  base: RevisionSide;
  /** Undefined means the newer side is the live buffer: the overlay keeps
   * re-diffing as the file is edited, like the HEAD diff. */
  head?: RevisionSide;
};

type CommitItem = vscode.QuickPickItem & { commit?: GitCommit; worktree?: boolean };

function commitItem(c: GitCommit): CommitItem {
  return {
    label: `$(git-commit) ${c.short}`,
    description: c.subject,
    detail: `${c.date} · ${c.author}`,
    commit: c,
  };
}

/** Ask for two revisions of the file. Resolves undefined when the user
 * escapes, when the file has no history, or when a chosen revision cannot
 * be read (the error is shown here). */
export async function pickRevisions(fsPath: string): Promise<RevisionPair | undefined> {
  const name = path.basename(fsPath);
  const commits = await gitLog(fsPath);
  if (commits.length === 0) {
    void vscode.window.showErrorMessage(`nautilus: ${name} has no git history (untracked, or not in a repository)`);
    return undefined;
  }

  const older = await vscode.window.showQuickPick<CommitItem>(commits.map(commitItem), {
    title: `Diff ${name} — older side`,
    placeHolder: "The revision to compare from (newest first)",
    matchOnDescription: true,
    matchOnDetail: true,
  });
  if (!older?.commit) return undefined;

  const newerItems: CommitItem[] = [
    { label: "$(edit) Working tree", description: "the file as it is now", worktree: true },
    ...commits.filter((c) => c.sha !== older.commit!.sha).map(commitItem),
  ];
  const newer = await vscode.window.showQuickPick<CommitItem>(newerItems, {
    title: `Diff ${name} — newer side (older: ${older.commit.short})`,
    placeHolder: "The revision to compare to",
    matchOnDescription: true,
    matchOnDetail: true,
  });
  if (!newer) return undefined;

  const baseSrc = await gitShow(fsPath, older.commit.sha, older.commit.path);
  if (baseSrc === undefined) {
    void vscode.window.showErrorMessage(`nautilus: ${name} could not be read at ${older.commit.short}`);
    return undefined;
  }
  const base: RevisionSide = { src: baseSrc, label: older.commit.short };
  if (newer.worktree || !newer.commit) return { base };

  const headSrc = await gitShow(fsPath, newer.commit.sha, newer.commit.path);
  if (headSrc === undefined) {
    void vscode.window.showErrorMessage(`nautilus: ${name} could not be read at ${newer.commit.short}`);
    return undefined;
  }
  return { base, head: { src: headSrc, label: newer.commit.short } };
}
