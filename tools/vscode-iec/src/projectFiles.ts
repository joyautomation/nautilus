// Where a project's IEC files live, for the extension features that only
// scan their text (live-value instance discovery). Anything that needs the
// project's COMPOSITION — which files are libraries, the prelude — asks
// `naut compose` instead (compose.ts).
// The rule is internal/stproject's: programs sit in the project root, and
// libraries are the root's PROGRAM-less files plus everything under lib/.
// The pure half (path tests, ordering) is in programSync.ts.

import * as vscode from "vscode";
import { inLibDir, isLibraryCandidate, LIB_DIR, sortPaths } from "./programSync";

async function exists(uri: vscode.Uri): Promise<boolean> {
  try {
    await vscode.workspace.fs.stat(uri);
    return true;
  } catch {
    return false;
  }
}

/** The directory a file composes against — stproject.ProjectRoot: its own
 * directory, unless it sits under the lib/ directory of the nearest
 * enclosing nautilus.yaml project, in which case that project's root. */
export async function projectDirFor(file: vscode.Uri): Promise<vscode.Uri> {
  const own = vscode.Uri.joinPath(file, "..");
  let dir = own;
  for (let i = 0; i < 32; i++) {
    if (await exists(vscode.Uri.joinPath(dir, "nautilus.yaml"))) {
      const rel = file.path.startsWith(dir.path + "/") ? file.path.slice(dir.path.length + 1) : "";
      return inLibDir(rel) ? dir : own;
    }
    const parent = vscode.Uri.joinPath(dir, "..");
    if (parent.path === dir.path) break;
    dir = parent;
  }
  return own;
}

/** Every file in the project root and under lib/ (recursively) whose name
 * matches `pattern`, as project-relative slash paths sorted bytewise. */
export async function projectFiles(root: vscode.Uri, pattern: RegExp): Promise<{ rel: string; uri: vscode.Uri }[]> {
  const out: string[] = [];
  const walk = async (dir: vscode.Uri, prefix: string): Promise<void> => {
    let entries: [string, vscode.FileType][];
    try {
      entries = await vscode.workspace.fs.readDirectory(dir);
    } catch {
      return;
    }
    for (const [name, kind] of entries) {
      const rel = prefix ? `${prefix}/${name}` : name;
      if (kind === vscode.FileType.Directory) {
        if (prefix ? isLibraryCandidate(rel + "/x") : name === LIB_DIR) {
          await walk(vscode.Uri.joinPath(dir, name), rel);
        }
      } else if (kind === vscode.FileType.File && pattern.test(name)) {
        out.push(rel);
      }
    }
  };
  await walk(root, "");
  return sortPaths(out).map((rel) => ({ rel, uri: vscode.Uri.joinPath(root, ...rel.split("/")) }));
}
