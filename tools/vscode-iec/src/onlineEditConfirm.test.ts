// Plain-Node tests for the online-edit confirmation wording (no vscode
// dependency). Run via `npm test` (compiles then executes with node:test).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { downloadConfirmMessage, forceDownloadConfirmMessage, rollbackConfirmMessage } from "./programSync";

const URL = "http://plant-line-3.local:8080";

test("downloadConfirmMessage names the controller, the program/POU, and its current hash", () => {
  const msg = downloadConfirmMessage(URL, "main.st", "Main", "abc123");
  assert.match(msg, /Download/);
  assert.match(msg, /main\.st \(Main\)/);
  assert.match(msg, new RegExp(URL.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(msg, /abc123/);
});

test("downloadConfirmMessage falls back to the file name alone when the POU is unknown", () => {
  const msg = downloadConfirmMessage(URL, "main.st", "", "abc123");
  assert.match(msg, /Download main\.st to/);
  assert.doesNotMatch(msg, /\(\)/);
});

test("forceDownloadConfirmMessage is just as explicit about the target as the modal it replaces", () => {
  const msg = forceDownloadConfirmMessage(URL, "main.st", "Main", "baseHash mismatch");
  assert.match(msg, /main\.st \(Main\)/);
  assert.match(msg, new RegExp(URL.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(msg, /baseHash mismatch/);
});

test("rollbackConfirmMessage names the program and the controller", () => {
  assert.equal(rollbackConfirmMessage(URL, "Main"), `Roll back Main on ${URL} to the previous program?`);
});

test("rollbackConfirmMessage falls back to a generic label when the POU is unknown", () => {
  assert.equal(rollbackConfirmMessage(URL, ""), `Roll back the program on ${URL} to the previous program?`);
});
