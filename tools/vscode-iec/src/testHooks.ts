// The vscode-bound half of the test hooks (the pure snapshot writer is
// testState.ts). Everything here is a no-op unless NAUTILUS_TEST_STATE is set.

import * as vscode from "vscode";
import { testState, TestSnapshot } from "./testState";

type Level = "info" | "warning" | "error";

function record(level: Level, args: unknown[]): void {
  const text = typeof args[0] === "string" ? args[0] : "";
  testState()?.notify(level, text);
}

/** Drop-in for vscode.window.show{Information,Warning,Error}Message that
 * also records the message for the test snapshot. */
export const notifyInfo: typeof vscode.window.showInformationMessage = ((...a: unknown[]) => {
  record("info", a);
  return (vscode.window.showInformationMessage as (...x: unknown[]) => unknown)(...a);
}) as typeof vscode.window.showInformationMessage;
export const notifyWarning: typeof vscode.window.showWarningMessage = ((...a: unknown[]) => {
  record("warning", a);
  return (vscode.window.showWarningMessage as (...x: unknown[]) => unknown)(...a);
}) as typeof vscode.window.showWarningMessage;
export const notifyError: typeof vscode.window.showErrorMessage = ((...a: unknown[]) => {
  record("error", a);
  return (vscode.window.showErrorMessage as (...x: unknown[]) => unknown)(...a);
}) as typeof vscode.window.showErrorMessage;

/** Name a status-bar item and mirror its text/tooltip into the snapshot;
 * pass undefined text when the item is hidden. */
export function mirrorStatus(item: vscode.StatusBarItem, name: string, shown: boolean): void {
  const t = testState();
  if (!t) return;
  t.setStatus(name, shown ? { text: item.text, tooltip: String(item.tooltip ?? "") } : undefined);
}

/** The open editors, as the snapshot wants them. */
export function openEditors(): TestSnapshot["openEditors"] {
  const out: TestSnapshot["openEditors"] = [];
  try {
    for (const g of vscode.window.tabGroups.all) {
      for (const tab of g.tabs) {
        const i = tab.input;
        if (i instanceof vscode.TabInputText) out.push({ uri: i.uri.toString(), viewType: "default" });
        else if (i instanceof vscode.TabInputCustom) out.push({ uri: i.uri.toString(), viewType: i.viewType });
        else if (i instanceof vscode.TabInputWebview) out.push({ uri: "", viewType: i.viewType });
      }
    }
  } catch {
    // never throw from a test hook
  }
  return out;
}
