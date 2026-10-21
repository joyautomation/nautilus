// The committed kind-file schema is the derivation of the scene schema's
// kindContract (kindSchema.ts); `npm run gen:kind-schema` after a change
// to the scene schema, or this fails.

import { strict as assert } from "node:assert";
import { test } from "node:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { deriveKindSchema, renderKindSchema } from "./kindSchema";

const dir = path.join(__dirname, "..", "schemas");
const sceneText = fs.readFileSync(path.join(dir, "nautilus-scene.schema.json"), "utf8");

test("schemas/nautilus-kind.schema.json is the derivation of the scene schema", () => {
  const committed = fs.readFileSync(path.join(dir, "nautilus-kind.schema.json"), "utf8");
  assert.equal(committed, renderKindSchema(sceneText), "run `npm run gen:kind-schema`");
});

test("a kind file takes the kind contract's keys minus the two that stay inline", () => {
  const scene = JSON.parse(sceneText);
  const kind = deriveKindSchema(scene);
  const contract = Object.keys(scene.definitions.kindContract.properties).sort();
  const file = Object.keys(kind.properties as object).sort();
  assert.deepEqual(file, contract.filter((k) => k !== "component" && k !== "assembly"));
  // Every $ref the properties use is carried along.
  const text = JSON.stringify(kind.properties);
  for (const m of text.matchAll(/#\/definitions\/(\w+)/g)) assert.ok((kind.definitions as object)[m[1] as keyof object], `missing definition ${m[1]}`);
  assert.equal((kind as { additionalProperties: boolean }).additionalProperties, false);
});

test("the scene schema's kinds entry takes the contract or a kind file path", () => {
  const scene = JSON.parse(sceneText);
  const entry = scene.properties.kinds.additionalProperties;
  assert.ok(Array.isArray(entry.oneOf) && entry.oneOf.length === 2, JSON.stringify(entry));
  assert.equal(entry.oneOf[0].$ref, "#/definitions/kindContract");
  const re = new RegExp(entry.oneOf[1].pattern);
  for (const [p, ok] of [
    ["hmi/src/lib/kinds/pump.kind.json", true],
    ["pump.kind.json", true],
    ["/abs/pump.kind.json", false],
    ["../pump.kind.json", false],
    ["http://x/pump.kind.json", false],
    ["pump.json", false],
  ] as const)
    assert.equal(re.test(p), ok, p);
});
