// Whether the controller is starting up: compiling the project and about to
// scan. Only the playground sets it (web/platform.ts, while its engine
// compiles and starts the project the page opened with); a desktop's
// controller is a `naut run` the extension does not start, so there it is
// never set. The live pills read it so a controller that is on its way
// says "starting", not "offline".
import * as vscode from "vscode";

let starting = false;
const changed = new vscode.EventEmitter<void>();

/** Fires when the controller starts or finishes starting. */
export const onDidChangeControllerStarting = changed.event;

export function controllerStarting(): boolean {
  return starting;
}

export function setControllerStarting(value: boolean): void {
  if (value === starting) return;
  starting = value;
  changed.fire();
}
