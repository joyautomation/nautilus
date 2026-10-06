// The Live Values panel: a tree of the controller's tags and program locals,
// with their current values, refreshed off the same SSE stream that paints
// the inline pills. Tags carry an inline "Set value" pencil (the whole point
// — setting several values in a row without hunting each identifier in the
// code); locals are read-only (the program owns them). A tag whose value is a
// struct/array expands to its members, read-only.

import * as vscode from "vscode";
import { LiveValues } from "./liveValues";
import { formatValue } from "./scan";
import { forcedDescription } from "./forces";

const REFRESH_THROTTLE_MS = 500;

type Node =
  | { kind: "group"; label: string; settable: boolean; entries: [string, unknown][]; forces?: boolean }
  | { kind: "tag"; name: string; value: unknown; settable: boolean; forced?: string; forceRow?: boolean }
  | { kind: "member"; label: string; value: unknown };

export class LiveValuesView implements vscode.TreeDataProvider<Node> {
  private readonly changed = new vscode.EventEmitter<Node | undefined>();
  readonly onDidChangeTreeData = this.changed.event;
  private refreshTimer: NodeJS.Timeout | undefined;
  private readonly disposables: vscode.Disposable[] = [];

  constructor(private readonly live: LiveValues) {
    // A frame every 100 ms would thrash the tree; coalesce to twice a second.
    this.disposables.push(
      live.onDidChangeValues(() => {
        if (this.refreshTimer) return;
        this.refreshTimer = setTimeout(() => {
          this.refreshTimer = undefined;
          this.changed.fire(undefined);
        }, REFRESH_THROTTLE_MS);
      })
    );
  }

  getTreeItem(node: Node): vscode.TreeItem {
    if (node.kind === "group") {
      const item = new vscode.TreeItem(node.label, vscode.TreeItemCollapsibleState.Expanded);
      // The Forces group carries "Remove All Forces" on its row.
      item.contextValue = node.forces ? "nautilusForceGroup" : "nautilusGroup";
      if (node.forces) {
        item.iconPath = new vscode.ThemeIcon("lock", new vscode.ThemeColor("charts.orange"));
        item.tooltip = "Values held by a force against the field and the logic, until removed";
      }
      return item;
    }
    if (node.kind === "member") {
      const item = new vscode.TreeItem(node.label);
      item.description = formatValue(node.value);
      return item;
    }
    // A tag or local leaf.
    const compound = node.value !== null && typeof node.value === "object";
    const item = new vscode.TreeItem(
      node.name,
      compound ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None
    );
    item.description = formatValue(node.value);
    item.tooltip = `${node.name} = ${formatValue(node.value)}`;
    // contextValue drives the inline actions (see package.json
    // view/item/context): only settable leaves get the pencil and Force…;
    // a forced one gets Remove Force instead of the pencil — a write to it
    // would be refused while the force holds.
    item.contextValue = node.settable ? (node.forced ? "nautilusTagForced" : "nautilusTag") : "nautilusLocal";
    // Carry the name so nautilus.setValue/force can read it off the element.
    (item as unknown as { tag: string }).tag = node.name;
    (item as unknown as { force?: string }).force = node.forced;
    if (node.forced) {
      // The F badge: Logix and TIA both mark a forced value in the list.
      item.description = forcedDescription(formatValue(node.value));
      item.iconPath = new vscode.ThemeIcon("lock", new vscode.ThemeColor("charts.orange"));
      item.tooltip = `${node.name} = ${formatValue(node.value)} — FORCED${node.forced !== node.name ? ` (${node.forced})` : ""}; held against the field and the logic until removed`;
    }
    // Click-to-edit: a settable scalar opens the Set Live Value input on a
    // plain row click, so the panel reads as a values EDITOR (the pencil is
    // the same action, for discoverability) — or, when forced, the Force…
    // input to change the forced value. Compound values and locals have
    // no command — clicking just expands/selects them.
    if (node.settable && !compound) {
      item.command = node.forced
        ? { command: "nautilus.force", title: "Change force", arguments: [{ tag: node.forced }] }
        : { command: "nautilus.setValue", title: "Set value", arguments: [{ tag: node.name }] };
    }
    return item;
  }

  getChildren(node?: Node): Node[] {
    const snap = this.live.snapshot();
    if (!node) {
      if (!snap.enabled) return [];
      const groups: Node[] = [];
      if (snap.forces.size) {
        groups.push({ kind: "group", label: `Forces (${snap.forces.size})`, settable: true, entries: [...snap.forces], forces: true });
      }
      if (snap.tags.length) groups.push({ kind: "group", label: "Tags", settable: true, entries: snap.tags });
      if (snap.locals.length) groups.push({ kind: "group", label: "Locals", settable: false, entries: snap.locals });
      return groups;
    }
    if (node.kind === "group") {
      return node.entries
        .slice()
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([name, value]) => ({
          kind: "tag",
          name,
          value,
          settable: node.settable,
          forced: node.settable ? (node.forces ? name : this.live.forcedFor(name)) : undefined,
          forceRow: node.forces,
        }));
    }
    if (node.kind === "tag" && node.value && typeof node.value === "object") {
      return members(node.value);
    }
    return [];
  }

  refresh(): void {
    this.changed.fire(undefined);
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
    if (this.refreshTimer) clearTimeout(this.refreshTimer);
  }
}

// members flattens one level of a struct/array value into read-only rows.
function members(value: object): Node[] {
  if (Array.isArray(value)) {
    return value.map((v, i) => ({ kind: "member", label: `[${i}]`, value: v }));
  }
  return Object.entries(value).map(([k, v]) => ({ kind: "member", label: k, value: v }));
}
