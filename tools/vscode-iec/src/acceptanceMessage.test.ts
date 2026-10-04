import { test } from "node:test";
import * as assert from "node:assert/strict";
import { failureText, RunResult } from "./acceptanceMessage";

// The scaffold's first test with its last expectation flipped (issue #145):
// step 3 starts on line 28 with its `given:`, the failing `expect:` is 30.
function failed(failure: RunResult["failure"]): RunResult {
  return { suite: "my-plant_test.yaml", name: "pump", line: 24, passed: false, scans: 3, elapsedMs: 300, failure };
}

const flipped = failed({
  step: 3,
  line: 30,
  stepLine: 28,
  atMs: 300,
  reason: "expectation failed",
  detail: "PumpRun = false, want true",
  trace: [{ name: "PumpRun", atMs: [200, 300], values: ["false", "false"] }],
});

test("the first line — the one VS Code shows inline — is the tag and its values", () => {
  const { text } = failureText(flipped);
  assert.equal(text.split("\n")[0], "PumpRun = false, want true");
});

test("the second line says where and when, and why", () => {
  const { text } = failureText(flipped);
  assert.equal(text.split("\n")[1], "step 3 (line 28), t=0.300s of virtual time — expectation failed");
});

test("the message anchors on the failing assertion, not the step", () => {
  assert.equal(failureText(flipped).line, 30);
});

test("the trajectory follows in the peek", () => {
  const lines = failureText(flipped).text.split("\n");
  assert.deepEqual(lines.slice(2), [
    "",
    "PumpRun",
    "      0.20s      0.30s",
    "      false      false",
  ]);
});

test("an older CLI (step line only, no stepLine) still reads well and anchors on the step", () => {
  const r = failed({ step: 3, line: 28, atMs: 300, reason: "expectation failed", detail: "PumpRun = false, want true" });
  const { text, line } = failureText(r);
  assert.equal(text, "PumpRun = false, want true\nstep 3, t=0.300s of virtual time — expectation failed");
  assert.equal(line, 28);
});

test("an `until` that never held keeps its reason on the second line", () => {
  const r = failed({
    step: 2, line: 31, stepLine: 29, atMs: 1000,
    reason: "never held within 1s", detail: "PumpRun = false, want true",
  });
  assert.equal(failureText(r).text.split("\n")[1], "step 2 (line 29), t=1.000s of virtual time — never held within 1s");
});

test("a failure with no detail and no step (a faulted scan) leads with its reason, unanchored", () => {
  const r = failed({ step: 0, line: 0, atMs: 500, reason: "1 logic error(s) during the run" });
  const { text, line } = failureText(r);
  assert.equal(text, "1 logic error(s) during the run\nt=0.500s of virtual time");
  assert.equal(line, undefined);
});

test("a result with no failure block still says it failed", () => {
  assert.deepEqual(failureText(failed(undefined)), { text: "failed" });
});
