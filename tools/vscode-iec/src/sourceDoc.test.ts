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
