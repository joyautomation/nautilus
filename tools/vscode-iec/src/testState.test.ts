import { test } from "node:test";
import * as assert from "node:assert/strict";
import { SnapshotWriter, initTestState, testState, emptySnapshot, WriterDeps } from "./testState";

function fakeDeps() {
  const files: string[] = [];
  let pending: (() => void) | undefined;
  let cancelled = 0;
  const deps: WriterDeps = {
    write: (_f, d) => void files.push(d),
    setTimer: (fn) => ((pending = fn), 1),
    clearTimer: () => void cancelled++,
  };
  return { deps, files, fire: () => { const p = pending; pending = undefined; p?.(); }, cancelled: () => cancelled };
}

test("a burst of updates coalesces into one write", () => {
  const f = fakeDeps();
  const w = new SnapshotWriter("x.json", 250, f.deps);
  w.update({ connected: true });
  w.update({ syncState: "sync" });
  w.notify("info", "hello");
  assert.equal(f.files.length, 0);
  f.fire();
  assert.equal(f.files.length, 1);
  const s = JSON.parse(f.files[0]);
  assert.equal(s.connected, true);
  assert.equal(s.syncState, "sync");
  assert.deepEqual(s.lastNotification, { level: "info", text: "hello" });
  assert.equal(s.version, 1);
});

test("flush writes immediately and cancels the pending write", () => {
  const f = fakeDeps();
  const w = new SnapshotWriter("x.json", 250, f.deps);
  w.update({ cliPath: "/bin/naut" });
  w.flush();
  assert.equal(f.files.length, 1);
  assert.equal(f.cancelled(), 1);
});

test("status entries are keyed by name, prefixed in the tooltip, and removable", () => {
  const w = new SnapshotWriter("x.json", 250, fakeDeps().deps);
  w.setStatus("nautilus.live", { text: "$(pulse) nautilus: live", tooltip: "Streaming" });
  w.setStatus("nautilus.sync", { text: "a", tooltip: "b" });
  w.setStatus("nautilus.live", { text: "$(debug-disconnect) nautilus: offline", tooltip: "No frames" });
  assert.deepEqual(w.current.statusBar.map((s) => s.name), ["nautilus.live", "nautilus.sync"]);
  assert.equal(w.current.statusBar[0].tooltip, "[nautilus.live] No frames");
  w.setStatus("nautilus.live", undefined);
  assert.deepEqual(w.current.statusBar.map((s) => s.name), ["nautilus.sync"]);
});

test("errors set lastError; later non-errors leave it", () => {
  const w = new SnapshotWriter("x.json", 250, fakeDeps().deps);
  w.notify("error", "boom");
  w.notify("warning", "careful");
  assert.equal(w.current.lastError, "boom");
  assert.equal(w.current.lastNotification?.level, "warning");
});

test("a failing write never throws", () => {
  const deps: WriterDeps = { write: () => { throw new Error("disk full"); }, setTimer: () => 1, clearTimer: () => {} };
  const w = new SnapshotWriter("x.json", 250, deps);
  assert.doesNotThrow(() => w.flush());
});

test("initTestState is off without the env var and on with it", () => {
  assert.equal(initTestState({}), undefined);
  assert.equal(testState(), undefined);
  const os = require("os"), path = require("path"), fs = require("fs");
  const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "ts-")), "sub", "state.json");
  const w = initTestState({ NAUTILUS_TEST_STATE: file, NAUTILUS_TEST_FAST: "1" });
  assert.ok(w);
  const written = JSON.parse(fs.readFileSync(file, "utf8"));
  assert.equal(written.fast, true);
  assert.deepEqual(Object.keys(written).sort(), Object.keys(emptySnapshot()).sort());
});
