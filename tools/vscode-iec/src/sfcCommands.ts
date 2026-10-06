// Online SFC commands — "Set Active Step" and "Fire Transition" — from the
// SFC diagram's context menu (webview/context, keyed on the step/transition
// under the pointer: the diagram tags those elements with data-vscode-context)
// or from the palette, which picks from the controller's own chart list
// (GET /api/sfc). The work is the controller's: runtime/sfccmd.go.

import * as vscode from "vscode";
import { LiveValues } from "./liveValues";
import { notifyError, notifyInfo } from "./testHooks";

/** What the diagram's data-vscode-context carries for a step/transition. */
type DiagramContext = {
  webview?: string;
  nautilusSfcStep?: string;
  nautilusSfcTransition?: string;
};

type Chart = {
  task: string;
  pou: string;
  steps: { name: string; active: boolean; initial?: boolean }[];
  transitions: { id: string; name?: string; line: number; from: string[]; to: string[]; enabled: boolean }[];
};

function runtimeUrl(): string {
  return vscode.workspace
    .getConfiguration("nautilus")
    .get<string>("runtimeUrl", "http://localhost:8080")
    .replace(/\/+$/, "");
}

/** The POU of the SFC document the active diagram tab shows, so a chart in
 * a multi-chart resource is addressed exactly (the controller otherwise
 * resolves by step name, which two charts may share). */
async function activePou(): Promise<string | undefined> {
  const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
  let uri: vscode.Uri | undefined;
  if (input instanceof vscode.TabInputCustom || input instanceof vscode.TabInputText) uri = input.uri;
  if (!uri || !/\.sfc$/i.test(uri.path)) return undefined;
  try {
    const doc = await vscode.workspace.openTextDocument(uri);
    return /^\s*PROGRAM\s+([A-Za-z_][A-Za-z0-9_]*)/im.exec(doc.getText())?.[1];
  } catch {
    return undefined;
  }
}

async function charts(): Promise<Chart[] | undefined> {
  try {
    const res = await fetch(runtimeUrl() + "/api/sfc");
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
    return ((await res.json()) as { charts: Chart[] }).charts;
  } catch (e) {
    void notifyError(`nautilus: could not read the controller's charts at ${runtimeUrl()} — ${String(e)}`);
    return undefined;
  }
}

async function pickChart(pou?: string): Promise<Chart | undefined> {
  const all = await charts();
  if (!all) return undefined;
  if (all.length === 0) {
    void notifyInfo("nautilus: the controller runs no SFC chart");
    return undefined;
  }
  const mine = pou ? all.find((c) => c.pou.toLowerCase() === pou.toLowerCase()) : undefined;
  if (mine) return mine;
  if (all.length === 1) return all[0];
  const pick = await vscode.window.showQuickPick(
    all.map((c) => ({ label: c.pou, description: `task ${c.task}`, chart: c })),
    { title: "nautilus: Which chart?" }
  );
  return pick?.chart;
}

export async function setActiveStep(live: LiveValues, arg?: DiagramContext | string): Promise<void> {
  const pou = await activePou();
  let step = typeof arg === "string" ? arg : arg?.nautilusSfcStep;
  let chartPou = pou;
  if (!step) {
    const chart = await pickChart(pou);
    if (!chart) return;
    chartPou = chart.pou;
    const pick = await vscode.window.showQuickPick(
      chart.steps.map((s) => ({ label: s.name, description: s.active ? "active" : undefined })),
      { title: `nautilus: Set the active step of ${chart.pou} (jumps the chart once)` }
    );
    if (!pick) return;
    step = pick.label;
  }
  await live.sfcSetStep(step, chartPou);
}

export async function fireTransition(live: LiveValues, arg?: DiagramContext | string): Promise<void> {
  const pou = await activePou();
  let id = typeof arg === "string" ? arg : arg?.nautilusSfcTransition;
  let chartPou = pou;
  if (!id) {
    const chart = await pickChart(pou);
    if (!chart) return;
    chartPou = chart.pou;
    const pick = await vscode.window.showQuickPick(
      chart.transitions.map((t) => ({
        label: t.id,
        description: `${t.from.join(", ")} → ${t.to.join(", ")}`,
        detail: t.enabled ? "enabled — fires once on the next scan" : "not enabled: a source step is not active",
      })),
      { title: `nautilus: Fire a transition of ${chart.pou} (once)` }
    );
    if (!pick) return;
    id = pick.label;
  }
  await live.sfcFireTransition(id, chartPou);
}
