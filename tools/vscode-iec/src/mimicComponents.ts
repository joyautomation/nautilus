// *.component.json sidecars: the extension-host half. Discovery, the live
// project-wide index, and where a ports edit from the mimic editor's ports
// mode ('p' on a selected equipment instance) should land. The aggregation/
// dedup math itself is pure and lives in mimicComponentIndex.ts (testable
// without vscode); this file is the vscode wiring around it — findFiles, a
// FileSystemWatcher, WorkspaceEdit.
//
// Discovery: every `**/*.component.json` in the workspace (node_modules/
// build output excluded), name-keyed — component names are already unique
// in the registry, so there's no per-directory scoping the way the old
// mimic.components.json walked up toward a workspace root. Two sidecars
// for the same component name is possible (copy-paste, two packages both
// shipping a same-named component) and resolved deterministically — see
// aggregateComponentFiles's shortest-path-wins rule.
//
// Write target for a ports edit: an existing sidecar for that component
// wins outright; otherwise a new `{Name}.component.json` is created next to
// `{Name}.svelte` if the workspace has one (keeps the override beside the
// component it describes), else next to the .mimic.json being edited (last
// resort — still works for built-ins and components this workspace
// doesn't define the source of).

import * as vscode from "vscode";
import {
  aggregateComponentFiles,
  patchComponentPortsText,
  validatePortList,
  type ComponentFile,
  type ComponentsManifest,
  type Port,
} from "./mimicComponentIndex";

export const COMPONENT_GLOB = "**/*.component.json";
export const EXCLUDE_GLOB = "**/{node_modules,.git,out,dist,build,.svelte-kit}/**";

/** A sidecar's CURRENT text: the open buffer's (unsaved edits included)
 * when VS Code has the file open, the disk copy otherwise. Every read that
 * feeds a later edit must go through here — a disk read of a dirty buffer
 * followed by a whole-file replace would throw away the unsaved edits. */
async function readText(uri: vscode.Uri): Promise<string> {
  const open = vscode.workspace.textDocuments.find((d) => d.uri.toString() === uri.toString());
  if (open) return open.getText();
  try {
    return new TextDecoder().decode(await vscode.workspace.fs.readFile(uri));
  } catch {
    return "";
  }
}

/** The live, workspace-wide index of every *.component.json — one instance
 * shared by every open mimic editor panel, so a sidecar edited by another
 * panel, by hand, or by git, updates them all (see MimicEditorProvider). */
export class ComponentIndex {
  manifest: ComponentsManifest = {};
  private winnerUris = new Map<string, vscode.Uri>();
  private watcher: vscode.FileSystemWatcher | undefined;

  async refresh(): Promise<void> {
    const uris = await vscode.workspace.findFiles(COMPONENT_GLOB, EXCLUDE_GLOB);
    const files = await Promise.all(
      uris.map(async (uri): Promise<ComponentFile & { uri: vscode.Uri }> => ({
        uri,
        path: uri.fsPath,
        text: await readText(uri),
      }))
    );
    const { manifest, winners, warnings } = aggregateComponentFiles(files);
    for (const w of warnings) console.warn(w);
    this.manifest = manifest;
    const byPath = new Map(files.map((f) => [f.path, f.uri]));
    this.winnerUris = new Map(
      Object.entries(winners).flatMap(([name, path]) => {
        const uri = byPath.get(path);
        return uri ? [[name, uri] as const] : [];
      })
    );
  }

  /** The sidecar currently backing `component`'s metadata, if any. */
  locate(component: string): vscode.Uri | undefined {
    return this.winnerUris.get(component);
  }

  /** Rebuild on any *.component.json create/change/delete anywhere in the
   * workspace and notify. Callers dispose the returned handle once, with
   * the extension. */
  watch(onChange: () => void): vscode.Disposable {
    this.watcher = vscode.workspace.createFileSystemWatcher(COMPONENT_GLOB);
    const fire = () => void this.refresh().then(onChange);
    // The index reads open buffers (readText), so an unsaved edit to an open
    // sidecar — by hand or from the component editor — refreshes it too, as
    // does closing one without saving (the buffer's text is gone; disk wins).
    let debounce: NodeJS.Timeout | undefined;
    const isSidecar = (doc: vscode.TextDocument) => doc.uri.path.endsWith(".component.json");
    const fireSoon = (doc: vscode.TextDocument) => {
      if (!isSidecar(doc)) return;
      if (debounce) clearTimeout(debounce);
      debounce = setTimeout(fire, 150);
    };
    const subs = [
      this.watcher.onDidCreate(fire),
      this.watcher.onDidChange(fire),
      this.watcher.onDidDelete(fire),
      vscode.workspace.onDidChangeTextDocument((e) => fireSoon(e.document)),
      vscode.workspace.onDidCloseTextDocument(fireSoon),
    ];
    const watcher = this.watcher;
    return {
      dispose: () => {
        if (debounce) clearTimeout(debounce);
        subs.forEach((s) => s.dispose());
        watcher.dispose();
      },
    };
  }
}

/** Find `{component}.svelte` in the workspace — shortest path wins on
 * multiple matches (same determinism as sidecar dedup). Undefined if the
 * component has no discoverable Svelte source (a kit built-in, or a custom
 * component this workspace doesn't define). Exported for userComponents.ts,
 * which resolves the same `{Name}.svelte` for a different reason (compiling
 * it, rather than finding where to write its ports sidecar) — one lookup,
 * not two. */
export async function locateComponentSource(component: string): Promise<vscode.Uri | undefined> {
  const uris = await vscode.workspace.findFiles(`**/${component}.svelte`, EXCLUDE_GLOB);
  if (!uris.length) return undefined;
  return [...uris].sort(
    (a, b) => a.fsPath.length - b.fsPath.length || a.fsPath.localeCompare(b.fsPath)
  )[0];
}

/** Every `{Name}.svelte` in the workspace, by component name — the full set
 * a custom component could be, independent of whether any open document
 * references it yet or a sidecar already exists for it. Used to offer
 * "Edit Component Ports…" on a component the user has only just authored
 * (no *.mimic.json equipment references it yet, so userComponents.ts's
 * request()-driven discovery has never seen it, and no sidecar exists
 * either — see editComponentPorts.ts). Built-ins are excluded by the
 * caller (they have no .svelte source in the workspace anyway). */
export async function discoverSvelteComponentNames(): Promise<string[]> {
  const uris = await vscode.workspace.findFiles("**/*.svelte", EXCLUDE_GLOB);
  const names = new Set<string>();
  for (const uri of uris) {
    const base = uri.fsPath.split(/[/\\]/).pop() ?? "";
    if (base.endsWith(".svelte")) names.add(base.slice(0, -".svelte".length));
  }
  return [...names].sort();
}

async function newSidecarLocation(docUri: vscode.Uri, component: string): Promise<vscode.Uri> {
  const source = await locateComponentSource(component);
  const dir = source ? vscode.Uri.joinPath(source, "..") : vscode.Uri.joinPath(docUri, "..");
  return vscode.Uri.joinPath(dir, `${component}.component.json`);
}

/** Apply one component's shared-ports edit: read the existing sidecar's
 * CURRENT text (its open buffer, unsaved edits included — see readText),
 * patch JUST the `ports` key — every other key (future per-component
 * metadata) passes through untouched — and write back as a single
 * WorkspaceEdit, or delete the file outright if the edit leaves nothing
 * worth keeping. A sidecar whose text doesn't parse right now (hand edit
 * mid-keystroke) refuses with a message rather than being replaced.
 * `ports: null` with no existing sidecar is a no-op — there's nothing to
 * clear. Refreshes `index` on success so the caller's next broadcast
 * reflects the write immediately, without waiting on the watcher. */
export async function writeComponentPortsEdit(
  index: ComponentIndex,
  docUri: vscode.Uri,
  component: string,
  ports: Port[] | null
): Promise<{ ok: boolean; error?: string }> {
  if (ports !== null && !validatePortList(ports)) return { ok: false, error: "invalid ports list" };
  const existing = index.locate(component);
  if (!existing && ports === null) return { ok: true };
  const target = existing ?? (await newSidecarLocation(docUri, component));
  // Open (or reuse) the document so what we patch IS what we replace — the
  // disk copy may be behind a dirty buffer.
  // (A new-sidecar target that already exists — the index lagging a
  // create — is patched the same way, never overwritten blind.)
  let openDoc: vscode.TextDocument | undefined;
  const isOpen = vscode.workspace.textDocuments.some((d) => d.uri.toString() === target.toString());
  let onDisk = isOpen;
  if (!onDisk) {
    try {
      await vscode.workspace.fs.stat(target);
      onDisk = true;
    } catch {
      onDisk = false;
    }
  }
  if (onDisk) {
    try {
      openDoc = await vscode.workspace.openTextDocument(target);
    } catch {
      openDoc = undefined; // vanished between the check and now
    }
  }
  const res = patchComponentPortsText(openDoc?.getText() ?? "", ports);
  if ("error" in res) return { ok: false, error: `${component}.component.json: ${res.error}` };

  const edit = new vscode.WorkspaceEdit();
  if (res.text === null) {
    if (!openDoc) return { ok: true };
    edit.deleteFile(openDoc.uri, { ignoreIfNotExists: true });
  } else if (openDoc) {
    if (res.text === openDoc.getText()) return { ok: true };
    edit.replace(openDoc.uri, new vscode.Range(0, 0, openDoc.lineCount, 0), res.text);
  } else {
    edit.createFile(target, { overwrite: true, contents: Buffer.from(res.text, "utf8") });
  }
  const ok = await vscode.workspace.applyEdit(edit);
  if (ok) await index.refresh();
  return { ok };
}
