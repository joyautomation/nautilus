// "nautilus: Copy Kit Component to Project…" — start your own symbol from
// one the kit ships. Pick a kit component (the mimic built-ins first:
// Tank, Pump, …; then everything else @joyautomation/nautilus-hmi
// exports), name the copy (MyTank), and its .svelte lands in the
// project's src/lib as a custom component of the project's own: same
// source the kit's author wrote, relative imports rewritten to the
// package (copyKitComponentLogic.ts), a header saying where it came from.
// A mimic built-in's copy also gets its `{Name}.component.json` with the
// kit's default ports and sample props, so pipes connect where they did
// and the preview renders it mid-motion. The copy then opens with the
// preview pane beside it — change the drawing, see it as you type.
//
// What the command does NOT do: register the copy on a page (that is the
// page's `<Mimic registry={{ MyTank }}>`, one line the message spells
// out) or rewrite equipment in a *.mimic.json to use it (the mimic
// editor's props panel switches a component by name).
import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import { BUILTIN_COMPONENT_NAMES, BUILTIN_COMPONENT_PORTS, BUILTIN_DEMO_PROPS } from "./builtinComponents";
import { ComponentIndex, EXCLUDE_GLOB, writeComponentEntryEdit } from "./mimicComponents";
import { svelteKitRootFor } from "./pagePreviewLogic";
import {
  KIT_PACKAGE,
  copyHeader,
  parseIndexExports,
  rewriteKitImports,
  validateNewComponentName,
  withHeader,
} from "./copyKitComponentLogic";

const exists = (p: string) => fs.existsSync(p);

/** The nearest directory at or above `fsPath` holding a package.json. */
function packageRootFor(fsPath: string): string | null {
  let dir = path.dirname(fsPath);
  for (;;) {
    if (exists(path.join(dir, "package.json"))) return dir;
    const parent = path.dirname(dir);
    if (parent === dir) return null;
    dir = parent;
  }
}

/** The SvelteKit project the copy goes into: the active file's (by its
 * svelte.config.*, else its nearest package.json), else the workspace's
 * only one, else a pick among several. */
async function pickSvelteKitRoot(): Promise<string | undefined> {
  const active = vscode.window.activeTextEditor?.document.uri;
  if (active?.scheme === "file") {
    const root = svelteKitRootFor(active.fsPath, exists) ?? packageRootFor(active.fsPath);
    if (root) return root;
  }
  const configs = await vscode.workspace.findFiles("**/svelte.config.{js,ts,mjs,cjs}", EXCLUDE_GLOB);
  const roots = [...new Set(configs.map((u) => path.dirname(u.fsPath)))].sort();
  if (roots.length === 0) {
    void vscode.window.showErrorMessage(
      "nautilus: no SvelteKit project (svelte.config.*) in the workspace to copy into — open a file of the HMI project first."
    );
    return undefined;
  }
  if (roots.length === 1) return roots[0];
  const pick = await vscode.window.showQuickPick(
    roots.map((r) => ({ label: vscode.workspace.asRelativePath(r), root: r })),
    { title: "nautilus: Copy Kit Component — which project?", ignoreFocusOut: true }
  );
  return pick?.root;
}

type KitItem = vscode.QuickPickItem & { name: string };

/** The kit's shipped components, mimic built-ins first. */
async function listKitComponents(kitDir: string): Promise<KitItem[]> {
  const dir = path.join(kitDir, "dist", "components");
  let names: string[];
  try {
    names = (await fs.promises.readdir(dir)).filter((f) => f.endsWith(".svelte")).map((f) => f.slice(0, -".svelte".length));
  } catch {
    return [];
  }
  const builtins = BUILTIN_COMPONENT_NAMES.filter((n) => names.includes(n));
  const rest = names.filter((n) => !builtins.includes(n)).sort();
  return [
    ...builtins.map((name): KitItem => {
      const ports = BUILTIN_COMPONENT_PORTS[name] ?? [];
      return {
        name,
        label: name,
        description: "mimic equipment",
        detail: ports.length ? `ports: ${ports.map((p) => p.name).join(", ")}` : "no ports",
      };
    }),
    ...rest.map((name): KitItem => ({ name, label: name, description: "kit component" })),
  ];
}

export function registerCopyKitComponentCommand(index: ComponentIndex): vscode.Disposable {
  return vscode.commands.registerCommand("nautilus.copyKitComponent", async () => {
    const root = await pickSvelteKitRoot();
    if (!root) return;
    const kitDir = path.join(root, "node_modules", ...KIT_PACKAGE.split("/"));
    const items = await listKitComponents(kitDir);
    if (items.length === 0) {
      void vscode.window.showErrorMessage(
        `nautilus: ${KIT_PACKAGE} isn't installed in ${path.basename(root)} (no node_modules/${KIT_PACKAGE}/dist/components) — run npm install there first.`
      );
      return;
    }

    const pick = await vscode.window.showQuickPick(items, {
      title: "nautilus: Copy Kit Component to Project…",
      placeHolder: "Which kit component to start from",
      matchOnDescription: true,
      ignoreFocusOut: true,
    });
    if (!pick) return;

    const libDir = path.join(root, "src", "lib");
    let existing: string[] = [];
    try {
      existing = (await fs.promises.readdir(libDir)).filter((f) => f.endsWith(".svelte")).map((f) => f.slice(0, -".svelte".length));
    } catch {
      existing = [];
    }
    const name = await vscode.window.showInputBox({
      title: `nautilus: Copy ${pick.name} — name of the copy`,
      prompt: `A component of this project's own, in ${path.relative(root, libDir)} — the name equipment refers to it by`,
      value: `My${pick.name}`,
      validateInput: (v) => validateNewComponentName(v, BUILTIN_COMPONENT_NAMES, existing),
      ignoreFocusOut: true,
    });
    if (!name) return;
    const newName = name.trim();

    let source: string;
    let indexJs: string;
    let kitVersion = "?";
    try {
      source = await fs.promises.readFile(path.join(kitDir, "dist", "components", `${pick.name}.svelte`), "utf8");
      indexJs = await fs.promises.readFile(path.join(kitDir, "dist", "index.js"), "utf8");
      const pkg = JSON.parse(await fs.promises.readFile(path.join(kitDir, "package.json"), "utf8")) as { version?: string };
      kitVersion = pkg.version ?? kitVersion;
    } catch (err) {
      void vscode.window.showErrorMessage(`nautilus: couldn't read ${pick.name}.svelte from the installed kit — ${String(err)}`);
      return;
    }
    const rewritten = rewriteKitImports(source, parseIndexExports(indexJs));
    const text = withHeader(rewritten.text, copyHeader(pick.name, newName, kitVersion));

    const target = vscode.Uri.file(path.join(libDir, `${newName}.svelte`));
    const edit = new vscode.WorkspaceEdit();
    edit.createFile(target, { ignoreIfExists: false, contents: Buffer.from(text, "utf8") });
    if (!(await vscode.workspace.applyEdit(edit))) {
      void vscode.window.showErrorMessage(`nautilus: couldn't create ${vscode.workspace.asRelativePath(target)}`);
      return;
    }

    // A mimic built-in's copy keeps the kit's ports and sample props in its
    // own sidecar: pipes connect where they did, the preview has values.
    if (BUILTIN_COMPONENT_NAMES.includes(pick.name)) {
      const ports = BUILTIN_COMPONENT_PORTS[pick.name] ?? [];
      const demo = BUILTIN_DEMO_PROPS[pick.name];
      const res = await writeComponentEntryEdit(
        index,
        target,
        newName,
        { ...(ports.length ? { ports } : {}), ...(demo ? { preview: { props: demo } } : {}) },
        "the copy's sidecar"
      );
      if (!res.ok && res.error) void vscode.window.showWarningMessage(`nautilus: ${res.error}`);
    }

    // In the text column: the preview pane (a webview) may be the active
    // editor when the command runs, and the copy belongs beside it, not in it.
    await vscode.window.showTextDocument(target, {
      preserveFocus: false,
      viewColumn: vscode.window.activeTextEditor?.viewColumn ?? vscode.ViewColumn.One,
    });
    await vscode.commands.executeCommand("nautilus.component.preview");

    const where = vscode.workspace.asRelativePath(target);
    const unresolved = rewritten.unresolved.length
      ? ` It imports kit internals the package doesn't export — copy or replace these: ${rewritten.unresolved.join(" · ")}.`
      : "";
    void vscode.window.showInformationMessage(
      `nautilus: ${where} is yours to change. On a page: import ${newName} from '$lib/${newName}.svelte' and pass <Mimic registry={{ ${newName} }}>; equipment with "component": "${newName}" renders it.${unresolved}`
    );
  });
}
