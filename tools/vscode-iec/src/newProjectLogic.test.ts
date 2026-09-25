// Plain-Node tests for the "nautilus: Create Project…" command's pure
// logic (no vscode dependency — the actual execFile call in newProject.ts
// isn't worth mocking, cliInstall.test.ts-style).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { NEW_PROJECT_TEMPLATES, newProjectArgs, validateProjectName } from "./newProjectLogic";

test("validateProjectName: rejects empty/whitespace", () => {
  assert.equal(validateProjectName(""), "a name is required");
  assert.equal(validateProjectName("   "), "a name is required");
});

test("validateProjectName: rejects spaces and slashes, matching cmd/naut/new.go", () => {
  assert.equal(validateProjectName("water plant"), "no spaces or slashes");
  assert.equal(validateProjectName("water/plant"), "no spaces or slashes");
  assert.equal(validateProjectName("water\\plant"), "no spaces or slashes");
});

test("validateProjectName: accepts an ordinary name", () => {
  assert.equal(validateProjectName("water-plant"), undefined);
  assert.equal(validateProjectName("  water-plant  "), undefined);
});

test("newProjectArgs: builds naut new's non-interactive argv", () => {
  assert.deepEqual(newProjectArgs("water-plant", "demo"), [
    "new",
    "water-plant",
    "--no-input",
    "--template",
    "demo",
  ]);
});

test("NEW_PROJECT_TEMPLATES: covers every template naut new supports, demo first", () => {
  assert.deepEqual(
    NEW_PROJECT_TEMPLATES.map((t) => t.template),
    ["demo", "minimal", "sdk", "sdk-demo"]
  );
});
