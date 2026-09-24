/// <reference types="node" />
// Pure-math coverage for resolvePorts()'s precedence chain — instance
// override -> project sidecar entry (for ANY component name, built-in or
// custom) -> registry's built-in default. No svelte/vscode imports, so (like
// portsGestures.test.ts next door) it runs directly with
// `node --experimental-strip-types --test` over this file. Not wired into
// the extension's `npm test` (this package has no build/dependencies of its
// own to compile that way) — run ad hoc:
// `node --experimental-strip-types --test src/mimic/ports.test.ts`
// from webview-ui/.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { resolvePorts, type ComponentsManifest } from "./ports.ts";

test("resolvePorts falls back to the registry's built-in default with no manifest/instance ports", () => {
  const ports = resolvePorts({ component: "Tank" }, null);
  assert.deepEqual(
    ports.map((p) => p.name),
    ["top", "left", "right", "bottom"]
  );
});

test("resolvePorts: a sidecar keyed by a BUILT-IN's name overrides its registry default", () => {
  // Tank.component.json in the project — same shape a custom component's
  // sidecar would have, just named after a built-in.
  const manifest: ComponentsManifest = {
    Tank: { ports: [{ name: "onlyPort", x: 0.5, y: 0.5 }] },
  };
  const ports = resolvePorts({ component: "Tank" }, manifest);
  assert.deepEqual(ports, [{ name: "onlyPort", x: 0.5, y: 0.5 }]);
});

test("resolvePorts: an explicit empty sidecar ports list for a built-in wins outright (no ports, not the builtin default)", () => {
  const manifest: ComponentsManifest = { Gauge: { ports: [] } };
  assert.deepEqual(resolvePorts({ component: "Gauge" }, manifest), []);
});

test("resolvePorts: instance override still wins over a built-in-name sidecar", () => {
  const manifest: ComponentsManifest = {
    Tank: { ports: [{ name: "sidecarPort", x: 0, y: 0 }] },
  };
  const instancePorts = [{ name: "instancePort", x: 1, y: 1 }];
  const ports = resolvePorts({ component: "Tank", ports: instancePorts }, manifest);
  assert.deepEqual(ports, instancePorts);
});

test("resolvePorts: a manifest entry with no `ports` key falls through to the built-in default", () => {
  const manifest: ComponentsManifest = { Tank: {} };
  const ports = resolvePorts({ component: "Tank" }, manifest);
  assert.deepEqual(
    ports.map((p) => p.name),
    ["top", "left", "right", "bottom"]
  );
});

test("resolvePorts: a component with no built-in default and no manifest entry resolves to []", () => {
  assert.deepEqual(resolvePorts({ component: "HeatExchanger" }, null), []);
  assert.deepEqual(resolvePorts({ component: "HeatExchanger" }, {}), []);
});

test("resolvePorts: a sidecar for a CUSTOM component name works the same way as a built-in one", () => {
  const manifest: ComponentsManifest = {
    HeatExchanger: {
      ports: [
        { name: "shellIn", x: 0.5, y: 0 },
        { name: "tubeIn", x: 0, y: 0.5 },
      ],
    },
  };
  const ports = resolvePorts({ component: "HeatExchanger" }, manifest);
  assert.deepEqual(ports.map((p) => p.name), ["shellIn", "tubeIn"]);
});
