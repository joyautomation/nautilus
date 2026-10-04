import { test } from "node:test";
import assert from "node:assert/strict";
import { afterTextTabClosed, nextSnapshot } from "./sourceGuard";

const dirty = { text: "STEP Extra:\nEND_STEP\n" };

test("Don't Save under an open diagram → restore the dirty text", () => {
  assert.equal(afterTextTabClosed(dirty, { docOpen: true, diagramOpen: true, isDirty: false, text: "" }), "restore");
});

test("Save → clean with the same text → nothing to do", () => {
  assert.equal(afterTextTabClosed(dirty, { docOpen: true, diagramOpen: true, isDirty: false, text: dirty.text }), "forget");
});

test("Cancel → still dirty → keep guarding", () => {
  assert.equal(afterTextTabClosed(dirty, { docOpen: true, diagramOpen: true, isDirty: true, text: dirty.text }), "keep");
});

test("no diagram left → the user really closed the file → forget", () => {
  assert.equal(afterTextTabClosed(dirty, { docOpen: true, diagramOpen: false, isDirty: false, text: "" }), "forget");
});

test("document disposed → forget", () => {
  assert.equal(afterTextTabClosed(dirty, { docOpen: false, diagramOpen: true, isDirty: false, text: "" }), "forget");
});

test("never dirty while the text tab was open → nothing to restore", () => {
  assert.equal(afterTextTabClosed(undefined, { docOpen: true, diagramOpen: true, isDirty: false, text: "" }), "forget");
});

test("the snapshot follows dirty states and ignores clean ones", () => {
  let s = nextSnapshot(undefined, false, "on disk");
  assert.equal(s, undefined, "a clean document takes no snapshot");
  s = nextSnapshot(s, true, "edit 1");
  s = nextSnapshot(s, true, "edit 2");
  assert.deepEqual(s, { text: "edit 2" });
  s = nextSnapshot(s, false, "on disk"); // the revert itself
  assert.deepEqual(s, { text: "edit 2" }, "a revert must not overwrite the last dirty text");
});
