import { strict as assert } from "node:assert";
import { test } from "node:test";
import { diagramKeyVerdict, diagramViewTypeFor, isDiagramKeyMessage } from "./diagramKeyPolicy";

test("isDiagramKeyMessage accepts only the three actions", () => {
  assert.equal(isDiagramKeyMessage({ type: "diagramKey", action: "undo" }), true);
  assert.equal(isDiagramKeyMessage({ type: "diagramKey", action: "redo" }), true);
  assert.equal(isDiagramKeyMessage({ type: "diagramKey", action: "save" }), true);
  assert.equal(isDiagramKeyMessage({ type: "diagramKey", action: "revert" }), false);
  assert.equal(isDiagramKeyMessage({ type: "edit", action: "undo" }), false);
  assert.equal(isDiagramKeyMessage(null), false);
  assert.equal(isDiagramKeyMessage(undefined), false);
});

test("undo/redo apply in the live view", () => {
  assert.equal(diagramKeyVerdict("undo", { diffing: false }), "apply");
  assert.equal(diagramKeyVerdict("redo", { diffing: false, readOnly: false }), "apply");
});

test("diff mode refuses undo/redo but still saves", () => {
  assert.equal(diagramKeyVerdict("undo", { diffing: true }), "refuseDiff");
  assert.equal(diagramKeyVerdict("redo", { diffing: true }), "refuseDiff");
  assert.equal(diagramKeyVerdict("save", { diffing: true }), "apply");
});

test("a read-only diagram (L5X) refuses undo/redo", () => {
  assert.equal(diagramKeyVerdict("undo", { diffing: false, readOnly: true }), "refuseReadOnly");
  assert.equal(diagramKeyVerdict("save", { diffing: false, readOnly: true }), "apply");
});

test("diagramViewTypeFor maps each language to its custom editor", () => {
  assert.equal(diagramViewTypeFor("iec-fbd"), "nautilus.fbdDiagram");
  assert.equal(diagramViewTypeFor("iec-ld"), "nautilus.ldDiagram");
  assert.equal(diagramViewTypeFor("iec-sfc"), "nautilus.sfcDiagram");
  assert.equal(diagramViewTypeFor("logix-l5x"), "nautilus.l5xDiagram");
  assert.equal(diagramViewTypeFor("iec-st"), undefined);
});
