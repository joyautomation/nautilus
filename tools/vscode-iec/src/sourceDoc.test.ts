import { test } from "node:test";
import assert from "node:assert/strict";
import { resolveSourceDoc, serialQueue } from "./sourceDoc";

const doc = (u: string) => ({ uri: { toString: () => u }, id: u });

test("resolveSourceDoc: an open document is returned without reopening", async () => {
  let reopened = 0;
  const a = doc("file:///a.fbd");
  const got = await resolveSourceDoc("file:///a.fbd", [doc("file:///b.fbd"), a], async () => {
    reopened++;
    return doc("file:///a.fbd");
  });
  assert.equal(got, a);
  assert.equal(reopened, 0);
});

test("resolveSourceDoc: a document with no editor left is reopened, not dropped", async () => {
  const again = doc("file:///a.fbd");
  const got = await resolveSourceDoc("file:///a.fbd", [], async () => again);
  assert.equal(got, again);
});

test("resolveSourceDoc: no tracked URI, or a file that can't be opened, is undefined", async () => {
  assert.equal(await resolveSourceDoc(undefined, [doc("x")], async () => doc("x")), undefined);
  const gone = await resolveSourceDoc("file:///deleted.fbd", [], async () => {
    throw new Error("cannot open");
  });
  assert.equal(gone, undefined);
});

test("serialQueue: jobs finish in arrival order even when an early one is slower", async () => {
  const run = serialQueue();
  const order: number[] = [];
  const slow = run(async () => {
    await new Promise((r) => setTimeout(r, 30));
    order.push(1);
  });
  const fast = run(() => order.push(2));
  await Promise.all([slow, fast]);
  assert.deepEqual(order, [1, 2]);
});

test("serialQueue: a throwing job doesn't block the next", async () => {
  const run = serialQueue();
  const seen: string[] = [];
  await run(() => {
    throw new Error("boom");
  });
  await run(() => seen.push("after"));
  assert.deepEqual(seen, ["after"]);
});

// The undo-then-save race in diagramKeys.ts: undo/redo hand focus to the
// source editor, run the command, wait for the edit to actually land, then
// hand focus back — and `applyDiagramKey` runs each key through a single
// serialQueue() (see diagramKeys.ts) so a save pressed right behind an undo
// can't start until all of that has finished. These fakes model the exact
// VS Code timing gap the real bug hid in: `vscode.commands.executeCommand`
// resolves once the command is DISPATCHED, before the extension host's own
// copy of the document has synced with the edit (a separate, later
// onDidChangeTextDocument). A job that doesn't wait for that sync — or
// that fires its async work without awaiting it — lets a save right behind
// it read the pre-undo text.

test("serialQueue: undo (command dispatch, then a later sync) then save — save sees the synced text", async () => {
  let text = "before";
  const run = serialQueue();
  const events: string[] = [];

  const undo = run(async () => {
    // The command dispatch resolves quickly...
    await new Promise((r) => setTimeout(r, 5));
    // ...but the extension host's copy of the document only catches up
    // with the edit a bit later — the gap `waitForDocSync` closes.
    await new Promise((r) => setTimeout(r, 30));
    text = "after-undo";
    events.push("undo-applied");
  });
  // Pressed right behind, the way Ctrl+S follows Ctrl+Z in the smoke test.
  const save = run(() => events.push(`save-sees:${text}`));

  await Promise.all([undo, save]);
  assert.deepEqual(events, ["undo-applied", "save-sees:after-undo"]);
});

test("serialQueue: a job that fires its async work without awaiting it lets the next job overtake it", async () => {
  // Reproduces the historical bug: applyDiagramKey's caller used to do
  // `void applyDiagramKey(...)` instead of awaiting it, so the queue
  // considered the undo "done" the moment it was fired, not once its edit
  // actually landed — exactly what let a save right behind it win.
  let text = "before";
  const run = serialQueue();
  const events: string[] = [];

  const undo = run(() => {
    void (async () => {
      await new Promise((r) => setTimeout(r, 30));
      text = "after-undo";
      events.push("undo-applied");
    })();
    // The job itself resolves immediately, without waiting for the above.
  });
  const save = run(() => events.push(`save-sees:${text}`));

  await Promise.all([undo, save]);
  // The queue already considers both jobs "done" here — that's the bug:
  // save ran (and saw the pre-undo text) before the fire-and-forget undo
  // above has even landed.
  assert.deepEqual(events, ["save-sees:before"]);
  await new Promise((r) => setTimeout(r, 40));
  assert.deepEqual(events, ["save-sees:before", "undo-applied"]);
});
