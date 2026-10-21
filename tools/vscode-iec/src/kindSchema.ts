// The kind file's JSON schema (schemas/nautilus-kind.schema.json) is
// DERIVED from the scene schema's kindContract, so the two cannot drift: a
// `{Name}.kind.json` holds the same keys a `kinds` entry does, minus the
// two that stay inline in the scene (a component, an assembly — design
// §3g). `npm run gen:kind-schema` writes it; kindSchema.test.ts holds the
// committed file to the derivation.

type Json = Record<string, unknown>;

const INLINE_ONLY = ["component", "assembly"];

/** Every `#/definitions/x` the value refers to, transitively. */
function refsOf(v: unknown, defs: Json, seen = new Set<string>()): Set<string> {
  if (Array.isArray(v)) {
    for (const x of v) refsOf(x, defs, seen);
  } else if (v && typeof v === "object") {
    for (const [k, x] of Object.entries(v as Json)) {
      if (k === "$ref" && typeof x === "string" && x.startsWith("#/definitions/")) {
        const name = x.slice("#/definitions/".length);
        if (!seen.has(name)) {
          seen.add(name);
          refsOf(defs[name], defs, seen);
        }
      } else refsOf(x, defs, seen);
    }
  }
  return seen;
}

export function deriveKindSchema(sceneSchema: Json): Json {
  const defs = sceneSchema.definitions as Json;
  const contract = defs.kindContract as Json;
  const properties: Json = {};
  for (const [k, v] of Object.entries(contract.properties as Json)) if (!INLINE_ONLY.includes(k)) properties[k] = v;
  const needed = [...refsOf(properties, defs)].sort();
  const definitions: Json = {};
  for (const n of needed) definitions[n] = defs[n];
  return {
    $schema: sceneSchema.$schema,
    title: "nautilus kind file",
    description:
      "A 3D kind in a file of its own ({Name}.kind.json, docs/design/spatial-hmi.md §3g): the same keys a scene's `kinds` entry takes — type, members, model, bounds, labelAt, status, drive — so one pump is declared once and placed in every scene that names the file (\"pump\": \"hmi/src/lib/kinds/pump.kind.json\"). A component or an assembly kind stays inline in the scene. Derived from nautilus-scene.schema.json's kindContract by `npm run gen:kind-schema`.",
    type: "object",
    additionalProperties: false,
    properties,
    definitions,
  };
}

export function renderKindSchema(sceneSchemaText: string): string {
  return JSON.stringify(deriveKindSchema(JSON.parse(sceneSchemaText) as Json), null, 2) + "\n";
}

if (process.argv.includes("--write")) {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const fs = require("fs") as typeof import("fs");
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const path = require("path") as typeof import("path");
  const dir = path.join(__dirname, "..", "schemas");
  const out = renderKindSchema(fs.readFileSync(path.join(dir, "nautilus-scene.schema.json"), "utf8"));
  fs.writeFileSync(path.join(dir, "nautilus-kind.schema.json"), out);
  console.log("wrote schemas/nautilus-kind.schema.json");
}
