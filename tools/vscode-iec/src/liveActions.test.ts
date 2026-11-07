// Plain-Node tests for the live tag commands' vscode-free parts: which tag a
// command acts on, Toggle Bit's write-or-refuse, and the no-tag warning.
// Run via `npm test` (compiles then executes with node:test).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { noTagMessage, tagOf, toggleBitDecision } from "./liveActions";

test("tagOf: a hover link's name, a Live Values item, a diagram element's right-click context", () => {
  assert.equal(tagOf("StartPB"), "StartPB");
  assert.equal(tagOf({ tag: "StartPB" }), "StartPB");
  assert.equal(tagOf({ name: "StartPB" }), "StartPB");
  assert.equal(tagOf({ nautilusTag: "StartPB" }), "StartPB");
  // {tag} wins over the others, as the Live Values item carries both.
  assert.equal(tagOf({ tag: "A", name: "B", nautilusTag: "C" }), "A");
  assert.equal(tagOf({ name: "B", nautilusTag: "C" }), "B");
});

test("tagOf: nothing named means no tag (the command falls back to the selection or cursor)", () => {
  assert.equal(tagOf(undefined), undefined);
  assert.equal(tagOf(""), undefined);
  assert.equal(tagOf({}), undefined);
});

test("toggleBitDecision: a BOOL's opposite, either way", () => {
  assert.deepEqual(toggleBitDecision("Run", { type: "BOOL", current: false, forced: false }), { write: true, shown: "TRUE" });
  assert.deepEqual(toggleBitDecision("Run", { type: "BOOL", current: true, forced: false }), { write: false, shown: "FALSE" });
  // The declared type is matched without case, and may be unknown.
  assert.deepEqual(toggleBitDecision("Run", { type: "bool", current: true, forced: false }), { write: false, shown: "FALSE" });
  assert.deepEqual(toggleBitDecision("Run", { current: false, forced: false }), { write: true, shown: "TRUE" });
});

test("toggleBitDecision: refuses a tag declared as something else, naming its type", () => {
  assert.deepEqual(toggleBitDecision("Speed", { type: "INT", typeLabel: "INT", current: 5, shown: "5", forced: false }), {
    refuse: "nautilus: Toggle Bit works on BOOL tags; Speed is INT",
  });
  assert.deepEqual(toggleBitDecision("Mode", { type: "Mode", typeLabel: "Mode · enum", current: 1, shown: "Run", forced: false }), {
    refuse: "nautilus: Toggle Bit works on BOOL tags; Mode is Mode · enum",
  });
});

test("toggleBitDecision: refuses with no live value yet, and a value that is not a boolean", () => {
  assert.deepEqual(toggleBitDecision("Run", { type: "BOOL", current: undefined, forced: false }), {
    refuse: "nautilus: no live value for Run yet (is the controller running?)",
  });
  // Type unknown but the live value is a number: say what it is.
  assert.deepEqual(toggleBitDecision("Count", { current: 3, shown: "3", forced: false }), {
    refuse: "nautilus: Toggle Bit works on BOOL tags; Count is 3",
  });
});

test("toggleBitDecision: refuses a forced tag (the force would win the next scan)", () => {
  assert.deepEqual(toggleBitDecision("StartPB", { type: "BOOL", current: true, forced: true }), {
    refuse: "nautilus: StartPB is forced, so a toggle would not stick; Unforce it first",
  });
});

test("noTagMessage: a focused diagram asks for a selection, a text editor for a cursor", () => {
  assert.equal(noTagMessage(true, "Toggle Bit", "a BOOL tag"), "nautilus: select a contact or coil in the diagram, then Toggle Bit");
  assert.equal(noTagMessage(false, "Toggle Bit", "a BOOL tag"), "nautilus: put the cursor on a BOOL tag, then Toggle Bit");
  assert.equal(noTagMessage(false, "Force…"), "nautilus: put the cursor on a tag, then Force…");
  assert.equal(noTagMessage(true, "Set Live Value"), "nautilus: select a contact or coil in the diagram, then Set Live Value");
});
