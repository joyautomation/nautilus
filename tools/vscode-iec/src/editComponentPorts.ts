// "Nautilus: Edit Component Ports…" — graphical port editing for BUILT-IN
// components (Tank, Pump, ...), reusing the same Component Editor a
// project's own custom components get. Built-ins have no *.component.json
// of their own until the user asks to override one; this command is that
// ask: pick a component (built-in, already-discovered custom, or any bare
// .svelte component in the workspace with no sidecar yet), then open its
// sidecar — creating one, prefilled with the component's current default
// ports, if it doesn't exist yet.
import * as vscode from "vscode";
import { ComponentIndex, EXCLUDE_GLOB, discoverSvelteComponentNames, writeComponentPortsEdit } from "./mimicComponents";
import { BUILTIN_COMPONENT_NAMES, BUILTIN_COMPONENT_PORTS } from "./builtinComponents";

/** Where a brand-new sidecar lands when there's no natural anchor yet
 * (writeComponentPortsEdit falls back to next to THIS uri's directory when
 * the component has no discoverable .svelte source — true for every
 * built-in). Prefers the active *.mimic.json, then any in the workspace,
 * then the first workspace folder itself. */
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

    const existing = index.locate(pick.name);
    if (existing) {
      await vscode.commands.executeCommand("vscode.openWith", existing, "nautilus.componentEditor");
      return;
    }

    const anchor = await pickSidecarAnchor();
    if (!anchor) {
      void vscode.window.showWarningMessage("nautilus: open a folder before editing component ports.");
      return;
    }

    // Prefill with the component's current defaults: the built-in table for
    // a built-in, or an empty list for a custom component (sidecar-
    // discovered, or a bare .svelte file with no sidecar yet — see
    // undiscoveredNames above). Either way the Component Editor lets ports
    // be added from there.
    const defaults = BUILTIN_COMPONENT_PORTS[pick.name] ?? [];
    const res = await writeComponentPortsEdit(index, anchor, pick.name, defaults);
    if (!res.ok) {
      void vscode.window.showErrorMessage(
        `nautilus: couldn't create ${pick.name}.component.json${res.error ? ` — ${res.error}` : ""}`
      );
      return;
    }
    const created = index.locate(pick.name);
    if (created) await vscode.commands.executeCommand("vscode.openWith", created, "nautilus.componentEditor");
  });
}
