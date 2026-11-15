// Whether the controller is starting up. Only the playground sets it
// (web/platform.ts, while its engine compiles and starts a project); a
// desktop's controller is a `naut run` the extension does not start, so
// there it is never set. The live pills read it so a controller that is on
// its way says "starting", not "offline".
//
// Starting is two stretches: compiling (a run in flight: there is nothing
// to ask yet), then a short wait after a run that succeeded, while the
// first answer is still on its way. A run that fails ends it at once, so a
// pill that cannot go live says "offline", not "starting". Every consumer
// shows live as soon as it has data, whatever this says.

/** How long after a good start a pill with no data yet still says
 * "starting": the live stream reconnects, the first scan answers. */
export const STARTING_GRACE_MS = 8000;

let compiling = 0;
// Some run in the current overlap started: the engine is scanning it, even
// if a later one failed (that one left the earlier program running).
let anyOk = false;
let graceUntil = 0;
let graceTimer: ReturnType<typeof setTimeout> | undefined;
// Plain listeners, not a vscode.EventEmitter: this module stays free of
// vscode so its tests run under node alone.
const listeners = new Set<() => void>();

/** Calls `fn` when starting begins or ends (the wait after a start
 * included); dispose the result to stop. */
export function onDidChangeControllerStarting(fn: () => void): { dispose(): void } {
  listeners.add(fn);
  return { dispose: () => void listeners.delete(fn) };
}

function fire(): void {
  for (const fn of [...listeners]) fn();
}

/** A run is in flight: the controller has nothing to answer yet. */
export function controllerCompiling(): boolean {
  return compiling > 0;
}

/** Compiling, or just started and not answering yet. */
export function controllerStarting(): boolean {
  return compiling > 0 || Date.now() < graceUntil;
}

/** A run began. Runs can overlap (F5 twice): each one ends with its own
 * `endControllerStart`. */
export function beginControllerStart(): void {
  compiling++;
  if (compiling === 1) anyOk = false;
  clearGrace();
  if (compiling === 1) fire();
}

/** A run ended: `ok` when the project started. */
export function endControllerStart(ok: boolean): void {
  if (compiling === 0) return;
  compiling--;
  anyOk ||= ok;
  if (compiling > 0) return;
  clearGrace();
  if (anyOk) {
    graceUntil = Date.now() + STARTING_GRACE_MS;
    graceTimer = setTimeout(() => {
      graceTimer = undefined;
      graceUntil = 0;
      fire();
    }, STARTING_GRACE_MS);
  }
  fire();
}

/** The controller was stopped: the wait after a start ends now, so the
 * pills say "offline" for a stopped controller. Runs in flight carry on. */
export function controllerStopped(): void {
  if (!graceTimer) return;
  clearGrace();
  fire();
}

/** Tests only: back to no runs and no wait. */
export function resetControllerStatusForTests(): void {
  compiling = 0;
  anyOk = false;
  clearGrace();
}

function clearGrace(): void {
  if (graceTimer) clearTimeout(graceTimer);
  graceTimer = undefined;
  graceUntil = 0;
}
