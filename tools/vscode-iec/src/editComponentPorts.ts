// "Nautilus: Edit Component Ports…" — pick a component (built-in, already-
// discovered custom, or any bare .svelte component in the workspace with
// no sidecar yet) and edit its ports on its render in the component
// preview pane (componentPreview.ts): a custom component from its .svelte,
// a built-in (Tank, Pump, ...) from the kit's registry, showing the kit's
// default ports until the first edit writes an override to
// `{Name}.component.json` — created then, next to the active mimic.
import * as vscode from "vscode";
import {
  ComponentIndex,
  EXCLUDE_GLOB,
  discoverSvelteComponentNames,
  locateComponentSource,
} from "./mimicComponents";
import { BUILTIN_COMPONENT_NAMES } from "./builtinComponents";
import type { PreviewArg } from "./componentPreview";

/** Where a built-in's brand-new sidecar lands when the pane's first edit
 * creates it (sidecarLocation falls back to THIS uri's directory when the
 * component has no discoverable .svelte source — true for every built-in).
 * Prefers the active *.mimic.json, then any in the workspace, then the
 * first workspace folder itself. */
async function pickSidecarAnchor(): Promise<vscode.Uri | undefined> {
  const active = vscode.window.activeTextEditor?.document.uri;
  if (active && active.path.endsWith(".mimic.json")) return active;
  const [first] = await vscode.workspace.findFiles("**/*.mimic.json", EXCLUDE_GLOB, 1);
  if (first) return first;
  const folder = vscode.workspace.workspaceFolders?.[0];
  // Only the parent directory of this synthetic uri is ever used.
  return folder ? vscode.Uri.joinPath(folder.uri, "mimic.json") : undefined;
}

type Item = vscode.QuickPickItem & { name: string };

export function registerEditComponentPortsCommand(index: ComponentIndex): vscode.Disposable {
  return vscode.commands.registerCommand("nautilus.editComponentPorts", async () => {
    const customNames = Object.keys(index.manifest)
      .filter((n) => !BUILTIN_COMPONENT_NAMES.includes(n))
      .sort();

    // Also offer a custom component that has no sidecar yet at all — a
    // .svelte file just authored, with no *.component.json and (often) no
    // *.mimic.json referencing it yet either, so it wouldn't otherwise show
    // up here (see mimicComponents.ts's discoverSvelteComponentNames doc
    // comment). A sidecar for it is created, prefilled with empty ports,
    // the first time it's picked below.
    const svelteNames = await discoverSvelteComponentNames();
    const undiscoveredNames = svelteNames
      .filter((n) => !BUILTIN_COMPONENT_NAMES.includes(n) && !customNames.includes(n))
      .sort();

    const items: Item[] = [
      ...BUILTIN_COMPONENT_NAMES.map(
        (name): Item => ({
          name,
          label: name,
          description: "built-in",
          detail: index.locate(name) ? "has a ports override" : undefined,
        })
      ),
      ...customNames.map(
        (name): Item => ({ name, label: name, description: "custom component", detail: "has a ports override" })
      ),
      ...undiscoveredNames.map(
        (name): Item => ({ name, label: name, description: "custom component", detail: "no ports override yet" })
      ),
    ];

    if (!items.length) {
      void vscode.window.showInformationMessage("nautilus: no components found to edit.");
      return;
    }

    const pick = await vscode.window.showQuickPick(items, {
      title: "nautilus: Edit Component Ports…",
      placeHolder: "Choose a component to edit its connection points",
      matchOnDescription: true,
    });
    if (!pick) return;

    // A built-in renders from the kit in the preview pane; its sidecar (an
    // override of the kit's ports) is the anchor when it exists, else the
    // pane's first edit creates one next to the active mimic.
    if (BUILTIN_COMPONENT_NAMES.includes(pick.name)) {
      const uri = index.locate(pick.name) ?? (await pickSidecarAnchor());
      if (!uri) {
        void vscode.window.showWarningMessage("nautilus: open a folder before editing component ports.");
        return;
      }
      const arg: PreviewArg = { name: pick.name, uri };
      await vscode.commands.executeCommand("nautilus.component.preview", arg);
      return;
    }

    // A custom component is edited on its live render — its .svelte opens
    // and the pane follows (sample props and ports in one place; the
    // sidecar is created by the first edit when there is none yet).
    const source = await locateComponentSource(pick.name);
    if (!source) {
      void vscode.window.showWarningMessage(
        `nautilus: no ${pick.name}.svelte in the workspace to render — edit ${pick.name}.component.json by hand`
      );
      return;
    }
    await vscode.window.showTextDocument(source, { preserveFocus: false });
    await vscode.commands.executeCommand("nautilus.component.preview");
  });
}
