// Plain-Node tests for the playground controller's "starting" state: runs
// in flight, the wait after a good start, a failed run ending it at once.
// Run via `npm test` (compiles then executes with node:test).

import { beforeEach, mock, test } from "node:test";
import * as assert from "node:assert/strict";
import {
  STARTING_GRACE_MS,
  beginControllerStart,
  controllerCompiling,
  controllerStarting,
  controllerStopped,
  endControllerStart,
  onDidChangeControllerStarting,
  resetControllerStatusForTests,
} from "./controllerStatus";

// The repo's @types/node predates the object form and Date; node 20.4+ has both.
mock.timers.enable({ apis: ["setTimeout", "Date"] } as never);
// Each test starts from no runs and no wait, whatever the last one left.
beforeEach(() => resetControllerStatusForTests());

function changes(): { count: () => number; dispose(): void } {
  let n = 0;
  const sub = onDidChangeControllerStarting(() => n++);
  return { count: () => n, dispose: () => sub.dispose() };
}

test("a good start: compiling, then starting for the wait, then not", () => {
  const c = changes();
  beginControllerStart();
  assert.equal(controllerCompiling(), true);
  assert.equal(controllerStarting(), true);
  endControllerStart(true);
  assert.equal(controllerCompiling(), false);
  assert.equal(controllerStarting(), true, "the first answer is still on its way");
  mock.timers.tick(STARTING_GRACE_MS - 1);
  assert.equal(controllerStarting(), true);
  mock.timers.tick(1);
  assert.equal(controllerStarting(), false);
  // begin, end, the wait running out: each told to listeners.
  assert.equal(c.count(), 3);
  c.dispose();
});

test("a failed start ends starting at once: the pill says offline, not starting", () => {
  const c = changes();
  beginControllerStart();
  endControllerStart(false);
  assert.equal(controllerStarting(), false);
  mock.timers.tick(STARTING_GRACE_MS);
  assert.equal(c.count(), 2, "no late change from a wait that never began");
  c.dispose();
});

test("overlapping runs: starting lasts until the last one ends", () => {
  beginControllerStart();
  beginControllerStart();
  endControllerStart(false);
  assert.equal(controllerCompiling(), true, "the second run is still compiling");
  endControllerStart(true);
  assert.equal(controllerCompiling(), false);
  assert.equal(controllerStarting(), true);
  mock.timers.tick(STARTING_GRACE_MS);
  assert.equal(controllerStarting(), false);
});

test("overlapping runs: one that started is waited for, even if a later one failed", () => {
  beginControllerStart();
  beginControllerStart();
  endControllerStart(true);
  endControllerStart(false);
  assert.equal(controllerStarting(), true, "the first program is coming up");
});

test("Stop ends the wait after a start at once; with no wait it says nothing", () => {
  beginControllerStart();
  endControllerStart(true);
  const c = changes();
  controllerStopped();
  assert.equal(controllerStarting(), false);
  assert.equal(c.count(), 1);
  controllerStopped();
  assert.equal(c.count(), 1);
  mock.timers.tick(STARTING_GRACE_MS);
  assert.equal(c.count(), 1, "the cancelled wait does not fire later");
  c.dispose();
});

test("a new run during the wait cancels it; an end with no run is ignored", () => {
  beginControllerStart();
  endControllerStart(true);
  beginControllerStart();
  mock.timers.tick(STARTING_GRACE_MS);
  assert.equal(controllerStarting(), true, "the old wait's timer did not end the new run");
  endControllerStart(false);
  assert.equal(controllerStarting(), false);
  endControllerStart(true);
  assert.equal(controllerStarting(), false);
});

test("a disposed listener hears nothing more", () => {
  const c = changes();
  c.dispose();
  beginControllerStart();
  endControllerStart(false);
  assert.equal(c.count(), 0);
});
