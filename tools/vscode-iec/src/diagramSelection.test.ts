// Plain-Node tests for diagramSelection.ts: the tag selected in each diagram
// webview, and which one "the tag here" means. Panels are fakes with the two
// members the module uses. Run via `npm test`.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { activeDiagramTag, diagramHasFocus, trackDiagramSelection } from "./diagramSelection";

type Fake = { active: boolean; onDidDispose: (f: () => void) => { dispose(): void }; dispose(): void };

function panel(active = false): Fake {
  const subs: Array<() => void> = [];
  return {
    active,
    onDidDispose: (f) => {
      subs.push(f);
      return { dispose() {} };
    },
    dispose() {
      for (const f of subs) f();
    },
  };
}

test("trackDiagramSelection: only a diagramSelection message is taken", () => {
  const p = panel(true);
  assert.equal(trackDiagramSelection(p, { type: "ldOp", op: {} }), false);
  assert.equal(trackDiagramSelection(p, undefined), false);
  assert.equal(trackDiagramSelection(p, { type: "diagramSelection", tag: "StartPB" }), true);
  assert.equal(activeDiagramTag(), "StartPB");
  p.dispose();
});

test("no diagram with focus: no tag, and the text cursor is what counts", () => {
  const p = panel(false);
  trackDiagramSelection(p, { type: "diagramSelection", tag: "StartPB" });
  assert.equal(diagramHasFocus(), false);
  assert.equal(activeDiagramTag(), undefined);
  p.dispose();
});

test("a focused diagram with nothing selected has focus but no tag (null, empty, not a string)", () => {
  const p = panel(true);
  for (const tag of [null, "", 7]) {
    trackDiagramSelection(p, { type: "diagramSelection", tag });
    assert.equal(diagramHasFocus(), true);
    assert.equal(activeDiagramTag(), undefined, JSON.stringify(tag));
  }
  p.dispose();
});

test("two diagrams: the focused one's selection, and it follows focus", () => {
  const a = panel(true);
  const b = panel(false);
  trackDiagramSelection(a, { type: "diagramSelection", tag: "A" });
  trackDiagramSelection(b, { type: "diagramSelection", tag: "B" });
  assert.equal(activeDiagramTag(), "A");
  a.active = false;
  b.active = true;
  assert.equal(activeDiagramTag(), "B");
  // A new selection replaces the panel's old one.
  trackDiagramSelection(b, { type: "diagramSelection", tag: "B2" });
  assert.equal(activeDiagramTag(), "B2");
  a.dispose();
  b.dispose();
});

test("a disposed panel is forgotten", () => {
  const p = panel(true);
  trackDiagramSelection(p, { type: "diagramSelection", tag: "StartPB" });
  assert.equal(diagramHasFocus(), true);
  p.dispose();
  assert.equal(diagramHasFocus(), false);
  assert.equal(activeDiagramTag(), undefined);
});
