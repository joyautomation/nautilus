import { strict as assert } from "node:assert";
import { test } from "node:test";
import { isProjectTagsPath } from "./projectTagsLogic";

test("the manifest and tag files count, in any project folder", () => {
  assert.ok(isProjectTagsPath("/w/lift/nautilus.yaml"));
  assert.ok(isProjectTagsPath("/w/lift/tags/station.yaml"));
  assert.ok(isProjectTagsPath("/w/lift/tags/pumps.yml"));
  assert.ok(isProjectTagsPath("C:\\w\\lift\\tags\\station.yaml"));
  assert.ok(isProjectTagsPath("/playground/nautilus.yaml"));
});

test("other yaml does not", () => {
  assert.ok(!isProjectTagsPath("/w/lift/sump_test.yaml"));
  assert.ok(!isProjectTagsPath("/w/lift/alarms.yaml"));
  assert.ok(!isProjectTagsPath("/w/lift/tags/readme.md"));
  assert.ok(!isProjectTagsPath("/w/lift/tags/deep/x.yaml"));
  assert.ok(!isProjectTagsPath("/w/lift/my-nautilus.yaml"));
});
