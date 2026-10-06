// Host side of two diagram-element features shared by the ladder, FBD and
// SFC editors (custom editors and preview panels alike):
//
//   #218 cross-reference — the webview posts {type:'xref', name, line,
//        endLine} for an element (Shift+F12, or its right-click "Find All
//        References"). findReferencesFromDiagram() finds that identifier in
//        the diagram's own text (xrefTarget.ts) and opens VS Code's
//        References view on it: the language server's references, which
//        report diagram files on their own lines and include the tag's
//        manifest declaration.
//
//   #216 descriptions — postDescriptions() sends the webview every name's
//        description ({type:'descriptions'}): the manifest's tags[].desc and
//        the file's own VAR comments, from ONE source, the language
//        server's `nautilus/descriptions` request (descriptions.go, on the
//        manifest read hover already caches). The webview shows them in
//        the element tooltips, and — `nautilus.diagram.showDescriptions` —
//        as a line under each ladder contact/coil.
//
// Each provider wires this with one line where it routes webview messages
// and one where it posts a model.

import * as vscode from "vscode";
import { lspClient } from "./lspClient";
import { describesLanguage, findIdentifier, normalizeDescriptions, type XrefMessage } from "./xrefTarget";

export { isXrefMessage } from "./xrefTarget";

// ── #218: cross-reference ──────────────────────────────────────────────────

/** The built-in references-view extension's API (vscode.references-view,
 * src/references-view.d.ts): a tree input it resolves and shows. */
type SymbolTreeApi = { setInput(input: SymbolTreeInput): void };
type SymbolTreeInput = {
  readonly contextValue: string;
  readonly title: string;
  readonly location: vscode.Location;
  resolve(): vscode.ProviderResult<SymbolTreeModel>;
  with(location: vscode.Location): SymbolTreeInput;
};
type SymbolTreeModel = {
  message: string | undefined;
  provider: vscode.TreeDataProvider<RefNode>;
  navigation?: {
    nearest(uri: vscode.Uri, position: vscode.Position): RefNode | undefined;
    next(from: RefNode): RefNode;
    previous(from: RefNode): RefNode;
    location(item: RefNode): vscode.Location | undefined;
  };
  highlights?: { getEditorHighlights(item: RefNode, uri: vscode.Uri): vscode.Range[] | undefined };
};

type FileNode = { kind: "file"; uri: vscode.Uri; refs: RefLeaf[] };
type RefLeaf = { kind: "ref"; file: FileNode; loc: vscode.Location; preview: string; hl: [number, number] };
type RefNode = FileNode | RefLeaf;

async function referencesView(): Promise<SymbolTreeApi | undefined> {
  const ext = vscode.extensions.getExtension<SymbolTreeApi>("vscode.references-view");
  if (!ext) return undefined;
  try {
    const api = ext.isActive ? ext.exports : await ext.activate();
    return typeof api?.setInput === "function" ? api : undefined;
  } catch {
    return undefined;
  }
}

/** The references view's input for one diagram identifier: resolving it
 * runs the reference provider at the identifier's position in the diagram
 * text, exactly as Shift+F12 in the text would. */
class DiagramReferences implements SymbolTreeInput {
  readonly contextValue = "nautilus.diagramReferences";
  constructor(
    readonly title: string,
    readonly location: vscode.Location
  ) {}

  with(location: vscode.Location): SymbolTreeInput {
    return new DiagramReferences(this.title, location);
  }

  async resolve(): Promise<SymbolTreeModel | undefined> {
    const locs =
      (await vscode.commands.executeCommand<vscode.Location[]>(
        "vscode.executeReferenceProvider",
        this.location.uri,
        this.location.range.start
      )) ?? [];
    const files = await referenceTree(locs);
    const total = files.reduce((n, f) => n + f.refs.length, 0);
    const flat = files.flatMap((f) => f.refs);
    const emitter = new vscode.EventEmitter<RefNode | undefined>();
    return {
      message:
        total === 0
          ? "No results."
          : `${total} result${total === 1 ? "" : "s"} in ${files.length} file${files.length === 1 ? "" : "s"}`,
      provider: {
        onDidChangeTreeData: emitter.event,
        getChildren: (el?: RefNode) => (!el ? files : el.kind === "file" ? el.refs : []),
        getParent: (el: RefNode) => (el.kind === "ref" ? el.file : undefined),
        getTreeItem: (el: RefNode) => {
          if (el.kind === "file") {
            const item = new vscode.TreeItem(el.uri, vscode.TreeItemCollapsibleState.Expanded);
            item.contextValue = "file-item";
            item.description = true;
            item.iconPath = vscode.ThemeIcon.File;
            return item;
          }
          const item = new vscode.TreeItem({ label: el.preview, highlights: [el.hl] });
          item.contextValue = "reference-item";
          item.tooltip = `${vscode.workspace.asRelativePath(el.loc.uri)}:${el.loc.range.start.line + 1}`;
          item.command = {
            command: "vscode.open",
            title: "Open Reference",
            arguments: [el.loc.uri, { selection: el.loc.range.with({ end: el.loc.range.start }) }],
          };
          return item;
        },
      },
      navigation: {
        nearest: (uri, pos) =>
          flat.find((r) => r.loc.uri.toString() === uri.toString() && r.loc.range.contains(pos)) ??
          flat.find((r) => r.loc.uri.toString() === uri.toString()),
        next: (from) => step(flat, from, 1),
        previous: (from) => step(flat, from, -1),
        location: (item) => (item.kind === "ref" ? item.loc : undefined),
      },
      highlights: {
        getEditorHighlights: (item, uri) =>
          item.kind === "ref" && item.file.uri.toString() === uri.toString() ? item.file.refs.map((r) => r.loc.range) : undefined,
      },
    };
  }
}

function step(flat: RefLeaf[], from: RefNode, by: number): RefNode {
  if (flat.length === 0) return from;
  const i = from.kind === "ref" ? flat.indexOf(from) : -1;
  return flat[(i + by + flat.length) % flat.length];
}

/** Group locations by file (in the order the server gave them) with a
 * one-line preview of each, the way the built-in references view shows
 * them. */
async function referenceTree(locs: vscode.Location[]): Promise<FileNode[]> {
  const byFile = new Map<string, FileNode>();
  for (const loc of locs) {
    const key = loc.uri.toString();
    let f = byFile.get(key);
    if (!f) {
      f = { kind: "file", uri: loc.uri, refs: [] };
      byFile.set(key, f);
    }
    let text = "";
    try {
      text = (await vscode.workspace.openTextDocument(loc.uri)).lineAt(loc.range.start.line).text;
    } catch {
      // a file that cannot be opened still lists, without a preview
    }
    const lead = text.length - text.trimStart().length;
    const preview = text.trim() || `line ${loc.range.start.line + 1}`;
    const s = Math.max(0, loc.range.start.character - lead);
    const e = Math.max(s, (loc.range.end.line === loc.range.start.line ? loc.range.end.character : text.length) - lead);
    f.refs.push({ kind: "ref", file: f, loc, preview, hl: [s, Math.min(e, preview.length)] });
  }
  for (const f of byFile.values()) f.refs.sort((a, b) => a.loc.range.start.compareTo(b.loc.range.start));
  return [...byFile.values()];
}

/** Find All References on a diagram element. */
export async function findReferencesFromDiagram(doc: vscode.TextDocument, msg: XrefMessage): Promise<void> {
  const at = findIdentifier(doc.getText(), msg.name, { line: msg.line, endLine: msg.endLine });
  if (!at) {
    vscode.window.setStatusBarMessage(`nautilus: "${msg.name}" is not a name in ${vscode.workspace.asRelativePath(doc.uri)}`, 4000);
    return;
  }
  const pos = new vscode.Position(at.line, at.character);
  const location = new vscode.Location(doc.uri, pos);
  const view = await referencesView();
  if (view) {
    view.setInput(new DiagramReferences("References", location));
    return;
  }
  // No references view (disabled built-in): the peek, in the text.
  const locs = (await vscode.commands.executeCommand<vscode.Location[]>("vscode.executeReferenceProvider", doc.uri, pos)) ?? [];
  await vscode.commands.executeCommand("editor.action.showReferences", doc.uri, pos, locs);
}

// ── #216: descriptions ─────────────────────────────────────────────────────

/** Webviews that have been sent descriptions, with the document they show
 * — re-sent when the setting or the project's tag files change. */
const targets = new Map<vscode.Webview, vscode.Uri>();

function showDescriptions(): boolean {
  return vscode.workspace.getConfiguration("nautilus").get<boolean>("diagram.showDescriptions", true);
}

/** Send the webview its document's descriptions. Never throws; a server
 * that is not up (or too old to answer) leaves the tooltips as they were. */
export async function postDescriptions(webview: vscode.Webview, doc: vscode.TextDocument): Promise<void> {
  if (!describesLanguage(doc.languageId)) return;
  targets.set(webview, doc.uri);
  await sendDescriptions(webview, doc.uri);
}

async function sendDescriptions(webview: vscode.Webview, uri: vscode.Uri): Promise<void> {
  const client = await lspClient();
  if (!client) return;
  try {
    const res = await client.sendRequest<unknown>("nautilus/descriptions", { textDocument: { uri: uri.toString() } });
    await webview.postMessage({ type: "descriptions", descriptions: normalizeDescriptions(res), show: showDescriptions() });
  } catch {
    // A disposed webview, or a CLI without the request: nothing to show.
    targets.delete(webview);
  }
}

/** Keep posted descriptions current: the setting toggles the ladder's
 * second line live, and a manifest/tag-file save re-reads them. */
export function registerDiagramDescriptions(context: vscode.ExtensionContext): void {
  const resendAll = () => {
    for (const [webview, uri] of targets) void sendDescriptions(webview, uri);
  };
  const watcher = vscode.workspace.createFileSystemWatcher("**/{nautilus.yaml,tags/*.yaml,tags/*.yml}");
  context.subscriptions.push(
    watcher,
    watcher.onDidChange(resendAll),
    watcher.onDidCreate(resendAll),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("nautilus.diagram.showDescriptions")) resendAll();
    })
  );
}
