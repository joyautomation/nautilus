// builtinComponents.ts is a hand-copy of three tables the extension host
// cannot import (they live in the browser-side kit and the webview's
// registry): the kit's BUILTIN_PORTS (hmi/src/lib/mimic.ts), and the
// webview registry's component set and DEMO_PROPS
// (webview-ui/src/mimic/registry.ts). A copy drifts — #466 moved the kit's
// default ports onto the parts of each drawing and the host kept the old
// box fractions, so the component preview pane showed (and the first edit
// wrote into the project's override) ports the runtime no longer uses.
// This test is the seam: it reads the sources as text and compares.
import { strict as assert } from "node:assert";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { BUILTIN_COMPONENT_NAMES, BUILTIN_COMPONENT_PORTS, BUILTIN_DEMO_PROPS } from "./builtinComponents";

// Compiled to out/; the extension root is one up.
const EXT = join(__dirname, "..");
const KIT_MIMIC = join(EXT, "..", "..", "hmi", "src", "lib", "mimic.ts");
const REGISTRY = join(EXT, "webview-ui", "src", "mimic", "registry.ts");

/** The object literal assigned to `export const <name>…= {…};` in a TS
 * source, evaluated. The tables are plain data (string/number/array/object
 * literals and comments), so the literal is valid JS once the type
 * annotation is gone. */
function literal(src: string, name: string): unknown {
  const start = src.search(new RegExp(`export const ${name}\\b[^=]*=\\s*\\{`));
  assert.ok(start >= 0, `no \`export const ${name} = {\` in the source — the table moved`);
  const open = src.indexOf("{", src.indexOf("=", start));
  let depth = 0;
  let end = open;
  for (; end < src.length; end++) {
    if (src[end] === "{") depth++;
    else if (src[end] === "}" && --depth === 0) break;
  }
  return new Function(`return (${src.slice(open, end + 1)});`)();
}

test("BUILTIN_COMPONENT_NAMES are the webview registry's components", () => {
  const src = readFileSync(REGISTRY, "utf8");
  const m = src.match(/export const registry\b[^=]*=\s*\{([^}]*)\}/);
  assert.ok(m, "no `export const registry = { … }` in registry.ts — the table moved");
  const names = m[1].split(",").map((s) => s.trim()).filter(Boolean);
  assert.deepEqual([...BUILTIN_COMPONENT_NAMES].sort(), [...names].sort());
});

test("BUILTIN_COMPONENT_PORTS match the kit's BUILTIN_PORTS (parts included)", () => {
  const kit = literal(readFileSync(KIT_MIMIC, "utf8"), "BUILTIN_PORTS") as Record<string, unknown>;
  for (const name of BUILTIN_COMPONENT_NAMES) {
    assert.ok(name in kit, `${name}: the kit's BUILTIN_PORTS has no entry`);
    assert.deepEqual(BUILTIN_COMPONENT_PORTS[name], kit[name], `${name}: the host's default ports differ from the kit's`);
  }
});

test("BUILTIN_DEMO_PROPS match the webview registry's DEMO_PROPS", () => {
  const demo = literal(readFileSync(REGISTRY, "utf8"), "DEMO_PROPS");
  assert.deepEqual(BUILTIN_DEMO_PROPS, demo);
});
