import { strict as assert } from "node:assert";
import { test } from "node:test";
import { ReadyGate, slotOf } from "./webviewReady";

function gate() {
  const sent: unknown[] = [];
  const g = new ReadyGate((m) => {
    sent.push(m);
    return Promise.resolve(true);
  });
  return { g, sent };
}

test("holds everything until the page says ready, then replays the latest state in order", () => {
  const { g, sent } = gate();
  g.post({ type: "liveValues", enabled: true, fresh: false });
  g.post({ type: "ldModel", model: 1 });
  g.post({ type: "diagnostics", diags: [] });
  g.post({ type: "liveValues", enabled: true, fresh: true });
  g.post({ type: "syncState", state: "differs" });
  assert.deepEqual(sent, []);
  g.onReady();
  assert.deepEqual(sent, [
    { type: "ldModel", model: 1 },
    { type: "diagnostics", diags: [] },
    { type: "syncState", state: "differs" },
    { type: "liveValues", enabled: true, fresh: true },
  ]);
});

test("after ready, posts pass straight through", () => {
  const { g, sent } = gate();
  g.onReady();
  g.post({ type: "model", model: 2 });
  assert.deepEqual(sent, [{ type: "model", model: 2 }]);
});

test("a reload (ready again) replays the CURRENT state, including a diff that replaced the model", () => {
  const { g, sent } = gate();
  g.onReady();
  g.post({ type: "sfcModel", model: 1 });
  g.post({ type: "liveValues", enabled: false });
  g.post({ type: "sfcDiff", base: 1, head: 2 });
  sent.length = 0;
  g.onReady();
  assert.deepEqual(sent, [{ type: "sfcDiff", base: 1, head: 2 }, { type: "liveValues", enabled: false }]);
});

test("an error rides beside the last good view until the next view clears it", () => {
  const { g, sent } = gate();
  g.post({ type: "model", model: 1 });
  g.post({ type: "error", message: "boom" });
  g.onReady();
  assert.deepEqual(sent, [{ type: "model", model: 1 }, { type: "error", message: "boom" }]);
  g.post({ type: "model", model: 2 });
  sent.length = 0;
  g.onReady();
  assert.deepEqual(sent, [{ type: "model", model: 2 }]);
});

test("reset (new html) holds messages again; one-shot messages are delivered once, never replayed", () => {
  const { g, sent } = gate();
  g.onReady();
  g.reset();
  g.post({ type: "somethingElse", n: 1 });
  g.post({ type: "ldModel", model: 3 });
  assert.deepEqual(sent, []);
  g.onReady();
  assert.deepEqual(sent, [{ type: "ldModel", model: 3 }, { type: "somethingElse", n: 1 }]);
  sent.length = 0;
  g.onReady();
  assert.deepEqual(sent, [{ type: "ldModel", model: 3 }]);
});

test("slots", () => {
  assert.equal(slotOf({ type: "ldDiff" }), "view");
  assert.equal(slotOf({ type: "liveValues" }), "liveValues");
  assert.equal(slotOf({ type: "whatever" }), undefined);
  assert.equal(slotOf(null), undefined);
});
