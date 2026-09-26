// Aggregation/dedup tests for *.component.json sidecars — pure logic, no
// vscode dependency, run with `npm test` (node --test over out/).

import { strict as assert } from "node:assert";
import { test } from "node:test";
import {
  aggregateComponentFiles,
  applyComponentPortsEdit,
  componentNameFromFilename,
  formatComponentEntry,
  isCandidateComponentSveltePath,
  paletteCustomComponents,
  parseComponentEntry,
  parseComponentEntryStrict,
  patchComponentPortsText,
  validatePortList,
  type ComponentFile,
} from "./mimicComponentIndex";

test("componentNameFromFilename extracts the name, or null for a non-match", () => {
  assert.equal(componentNameFromFilename("Tank.component.json"), "Tank");
  assert.equal(componentNameFromFilename("HeatExchanger.component.json"), "HeatExchanger");
  assert.equal(componentNameFromFilename("mimic.components.json"), null);
  assert.equal(componentNameFromFilename(".component.json"), null);
  assert.equal(componentNameFromFilename("Tank.svelte"), null);
  assert.equal(componentNameFromFilename("Tank.ports.json"), null);
});

test("isCandidateComponentSveltePath accepts an ordinary project component", () => {
  assert.equal(isCandidateComponentSveltePath("/proj/hmi/src/lib/ToProcess.svelte"), true);
  assert.equal(isCandidateComponentSveltePath("/proj/hmi/src/lib/components/Tank.svelte"), true);
  assert.equal(isCandidateComponentSveltePath("HeatExchanger.svelte"), true);
});

test("isCandidateComponentSveltePath rejects SvelteKit route/layout special files by basename", () => {
  assert.equal(isCandidateComponentSveltePath("/proj/src/routes/+page.svelte"), false);
  assert.equal(isCandidateComponentSveltePath("/proj/src/lib/+layout.svelte"), false);
  assert.equal(isCandidateComponentSveltePath("/proj/src/lib/+error.svelte"), false);
  assert.equal(isCandidateComponentSveltePath("/proj/src/lib/+page.server.svelte"), false);
});

test("isCandidateComponentSveltePath rejects anything under a src/routes/ directory, any depth, any basename", () => {
  assert.equal(isCandidateComponentSveltePath("/proj/hmi/src/routes/+page.svelte"), false);
  assert.equal(isCandidateComponentSveltePath("/proj/hmi/src/routes/dashboard/+page.svelte"), false);
  // A route's own local helper component (no leading +) is still route
  // structure, not a mimic component — excluded by directory, not basename.
  assert.equal(isCandidateComponentSveltePath("/proj/hmi/src/routes/Widget.svelte"), false);
  assert.equal(isCandidateComponentSveltePath("/proj/hmi/src/routes/dashboard/deep/Widget.svelte"), false);
  // Windows-style separators work the same way.
  assert.equal(isCandidateComponentSveltePath("C:\\proj\\hmi\\src\\routes\\+page.svelte"), false);
});

test("isCandidateComponentSveltePath: a \"routes\" directory NOT under src is not SvelteKit's and is not excluded", () => {
  assert.equal(isCandidateComponentSveltePath("/proj/hmi/lib/routes/Widget.svelte"), true);
});

test("parseComponentEntry is forgiving: missing/empty/malformed/non-object all read as {}", () => {
  assert.deepEqual(parseComponentEntry(""), {});
  assert.deepEqual(parseComponentEntry("   "), {});
  assert.deepEqual(parseComponentEntry("not json"), {});
  assert.deepEqual(parseComponentEntry("[1,2,3]"), {});
  assert.deepEqual(parseComponentEntry("null"), {});
  assert.deepEqual(parseComponentEntry('{"ports":[{"name":"a","x":0.5,"y":0}]}'), {
    ports: [{ name: "a", x: 0.5, y: 0 }],
  });
});

test("parseComponentEntryStrict throws on empty, malformed, or non-object text", () => {
  assert.throws(() => parseComponentEntryStrict("not json"));
  assert.throws(() => parseComponentEntryStrict("[1,2]"));
  assert.throws(() => parseComponentEntryStrict("null"));
  assert.deepEqual(parseComponentEntryStrict(""), {});
  assert.deepEqual(parseComponentEntryStrict('{"ports":[]}'), { ports: [] });
});

test("parseComponentEntryStrict rejects a ports list with duplicate names", () => {
  assert.throws(
    () => parseComponentEntryStrict('{"ports":[{"name":"a","x":0,"y":0},{"name":"a","x":1,"y":1}]}'),
    /ports/
  );
});

test("parseComponentEntryStrict rejects malformed port entries (missing name, bad numbers)", () => {
  assert.throws(() => parseComponentEntryStrict('{"ports":[{"x":0,"y":0}]}'), /ports/);
  assert.throws(() => parseComponentEntryStrict('{"ports":[{"name":"a","x":"0","y":0}]}'), /ports/);
  assert.throws(() => parseComponentEntryStrict('{"ports":[{"name":"","x":0,"y":0}]}'), /ports/);
  assert.throws(() => parseComponentEntryStrict('{"ports":[{"name":"a","x":0,"y":0,"dir":"sideways"}]}'), /ports/);
});

test("parseComponentEntryStrict accepts an explicit dir override", () => {
  assert.deepEqual(parseComponentEntryStrict('{"ports":[{"name":"out","x":1,"y":0.5,"dir":"right"}]}'), {
    ports: [{ name: "out", x: 1, y: 0.5, dir: "right" }],
  });
});

test("validatePortList: shape + name-uniqueness, the sidecar/op twin check", () => {
  assert.equal(validatePortList([]), true);
  assert.equal(validatePortList([{ name: "a", x: 0, y: 0 }]), true);
  assert.equal(
    validatePortList([
      { name: "a", x: 0, y: 0 },
      { name: "b", x: 1, y: 1 },
    ]),
    true
  );
  assert.equal(
    validatePortList([
      { name: "a", x: 0, y: 0 },
      { name: "a", x: 1, y: 1 },
    ]),
    false
  );
  assert.equal(validatePortList([{ name: "a", x: NaN, y: 0 }]), false);
  assert.equal(validatePortList("not an array"), false);
  assert.equal(validatePortList([[0, 0]]), false);
});

test("validatePortList: dir is optional, must be one of left/right/up/down when present", () => {
  assert.equal(validatePortList([{ name: "a", x: 0, y: 0.5, dir: "left" }]), true);
  assert.equal(validatePortList([{ name: "a", x: 0, y: 0.5, dir: "right" }]), true);
  assert.equal(validatePortList([{ name: "a", x: 0, y: 0.5, dir: "up" }]), true);
  assert.equal(validatePortList([{ name: "a", x: 0, y: 0.5, dir: "down" }]), true);
  assert.equal(validatePortList([{ name: "a", x: 0, y: 0.5, dir: "sideways" }]), false);
  assert.equal(validatePortList([{ name: "a", x: 0, y: 0.5 }]), true);
});

test("formatComponentEntry pretty-prints with a trailing newline", () => {
  assert.equal(
    formatComponentEntry({ ports: [{ name: "a", x: 0, y: 0 }] }),
    '{\n  "ports": [\n    {\n      "name": "a",\n      "x": 0,\n      "y": 0\n    }\n  ]\n}\n'
  );
});

test("aggregateComponentFiles resolves one sidecar per component name", () => {
  const files: ComponentFile[] = [
    {
      path: "/proj/src/lib/Tank.component.json",
      text: '{"ports":[{"name":"top","x":0.5,"y":0},{"name":"bottom","x":0.5,"y":1}]}',
    },
    { path: "/proj/src/lib/HeatExchanger.component.json", text: '{"ports":[{"name":"tubeIn","x":0,"y":0.5}]}' },
  ];
  const { manifest, winners, warnings } = aggregateComponentFiles(files);
  assert.deepEqual(manifest, {
    Tank: {
      ports: [
        { name: "top", x: 0.5, y: 0 },
        { name: "bottom", x: 0.5, y: 1 },
      ],
    },
    HeatExchanger: { ports: [{ name: "tubeIn", x: 0, y: 0.5 }] },
  });
  assert.deepEqual(winners, {
    Tank: "/proj/src/lib/Tank.component.json",
    HeatExchanger: "/proj/src/lib/HeatExchanger.component.json",
  });
  assert.deepEqual(warnings, []);
});

test("aggregateComponentFiles ignores files that don't match the *.component.json convention", () => {
  const files: ComponentFile[] = [
    { path: "/proj/mimic.components.json", text: '{"components":{}}' },
    { path: "/proj/src/heated-tank.mimic.json", text: "{}" },
    { path: "/proj/src/lib/Tank.ports.json", text: '{"ports":[[0,0]]}' },
  ];
  const { manifest, winners } = aggregateComponentFiles(files);
  assert.deepEqual(manifest, {});
  assert.deepEqual(winners, {});
});

test("duplicate {Name}.component.json: shortest path wins, with a warning naming both", () => {
  const files: ComponentFile[] = [
    { path: "/proj/packages/vendor/deep/nested/Tank.component.json", text: '{"ports":[{"name":"a","x":0,"y":0}]}' },
    { path: "/proj/Tank.component.json", text: '{"ports":[{"name":"a","x":0.5,"y":0.5}]}' },
  ];
  const { manifest, winners, warnings } = aggregateComponentFiles(files);
  assert.deepEqual(manifest.Tank, { ports: [{ name: "a", x: 0.5, y: 0.5 }] });
  assert.equal(winners.Tank, "/proj/Tank.component.json");
  assert.equal(warnings.length, 1);
  assert.match(warnings[0], /multiple Tank\.component\.json found/);
  assert.match(warnings[0], /proj\/packages\/vendor\/deep\/nested\/Tank\.component\.json/);
  assert.match(warnings[0], /using \/proj\/Tank\.component\.json/);
});

test("duplicate paths of equal length break the tie lexically, deterministically", () => {
  const files: ComponentFile[] = [
    { path: "/proj/b/Tank.component.json", text: '{"ports":[{"name":"a","x":1,"y":1}]}' },
    { path: "/proj/a/Tank.component.json", text: '{"ports":[{"name":"a","x":0,"y":0}]}' },
  ];
  const once = aggregateComponentFiles(files);
  const again = aggregateComponentFiles([...files].reverse());
  assert.equal(once.winners.Tank, "/proj/a/Tank.component.json");
  assert.deepEqual(once.manifest.Tank, { ports: [{ name: "a", x: 0, y: 0 }] });
  // Order-independent: the same file set always resolves to the same winner.
  assert.deepEqual(again.winners, once.winners);
  assert.deepEqual(again.manifest, once.manifest);
});

test("a malformed sidecar contributes an empty entry rather than throwing", () => {
  const files: ComponentFile[] = [{ path: "/proj/Gauge.component.json", text: "{not valid" }];
  const { manifest, warnings } = aggregateComponentFiles(files);
  assert.deepEqual(manifest.Gauge, {});
  assert.deepEqual(warnings, []);
});

test("paletteCustomComponents unions sidecar + doc-referenced + bare-svelte names, sorted and deduped", () => {
  assert.deepEqual(
    paletteCustomComponents(["HeatExchanger", "Widget"], ["Widget", "Conveyor"], [], new Set(["Tank", "Pump"])),
    ["Conveyor", "HeatExchanger", "Widget"]
  );
});

test("paletteCustomComponents excludes built-ins even when a sidecar or the doc names one", () => {
  // A Tank.component.json (a ports override) doesn't create a second
  // palette entry for "Tank" — it's still the one built-in.
  assert.deepEqual(
    paletteCustomComponents(["Tank"], ["Tank", "HeatExchanger"], [], new Set(["Tank", "Pump"])),
    ["HeatExchanger"]
  );
});

test("paletteCustomComponents: empty inputs resolve to an empty list", () => {
  assert.deepEqual(paletteCustomComponents([], [], [], new Set()), []);
});

test("paletteCustomComponents lists a bare .svelte component with no sidecar and not yet placed", () => {
  // The bug: a project's ToProcess.svelte with no ToProcess.component.json
  // and not referenced by any equipment in the open doc must still show up
  // so it can be placed — see discoverSvelteComponentNames.
  assert.deepEqual(paletteCustomComponents([], [], ["ToProcess"], new Set(["Tank", "Pump"])), ["ToProcess"]);
});

test("paletteCustomComponents: bare-svelte discovery unions with sidecar + doc names, deduped", () => {
  assert.deepEqual(
    paletteCustomComponents(["HeatExchanger"], ["Widget"], ["HeatExchanger", "ToProcess"], new Set(["Tank"])),
    ["HeatExchanger", "ToProcess", "Widget"]
  );
});

test("paletteCustomComponents excludes a built-in named as a bare .svelte discovery too", () => {
  assert.deepEqual(paletteCustomComponents([], [], ["Tank", "ToProcess"], new Set(["Tank", "Pump"])), ["ToProcess"]);
});

test("applyComponentPortsEdit sets, deletes, and preserves unrelated (future-metadata) keys", () => {
  assert.deepEqual(applyComponentPortsEdit({}, [{ name: "a", x: 0.5, y: 0 }]), {
    ports: [{ name: "a", x: 0.5, y: 0 }],
  });
  assert.deepEqual(
    applyComponentPortsEdit({ ports: [{ name: "a", x: 0, y: 0 }], defaults: { levelPct: 50 } }, null),
    { defaults: { levelPct: 50 } }
  );
  assert.deepEqual(applyComponentPortsEdit({ ports: [{ name: "a", x: 0, y: 0 }] }, null), {});
  assert.deepEqual(applyComponentPortsEdit({ palette: { category: "tanks" } }, [{ name: "a", x: 1, y: 1 }]), {
    palette: { category: "tanks" },
    ports: [{ name: "a", x: 1, y: 1 }],
  });
});

test("patchComponentPortsText refuses unparseable text instead of overwriting it", () => {
  const ports = [{ name: "in", x: 0, y: 0.5 }];
  const mid = '{\n  "ports": [\n    {"name": "in", "x": 0, ';
  const res = patchComponentPortsText(mid, ports);
  assert.ok("error" in res);
  assert.match((res as { error: string }).error, /valid/);
  // A syntactically fine but invalid entry refuses too (not replaced).
  assert.ok("error" in patchComponentPortsText('{"ports": 5, "defaults": {"a": 1}}', ports));
  assert.ok("error" in patchComponentPortsText("[1]", ports));
});

test("patchComponentPortsText patches only ports; other keys survive; empty result is null", () => {
  const res = patchComponentPortsText('{"defaults": {"a": 1}, "ports": []}', [{ name: "o", x: 1, y: 0.5 }]);
  assert.ok("text" in res);
  assert.deepEqual(JSON.parse((res as { text: string }).text), {
    defaults: { a: 1 },
    ports: [{ name: "o", x: 1, y: 0.5 }],
  });
  assert.deepEqual(patchComponentPortsText('{"ports": []}', null), { text: null });
  assert.deepEqual(patchComponentPortsText("", [{ name: "a", x: 0, y: 0 }]), {
    text: formatComponentEntry({ ports: [{ name: "a", x: 0, y: 0 }] }),
  });
});
