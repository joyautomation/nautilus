// web/runtimeModules.ts against the packages themselves: every client entry
// Svelte and the kit export is in the playground's runtime, so a new one
// (a Svelte upgrade) fails here, not as a component that will not load.

import { strict as assert } from "node:assert";
import { test } from "node:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { KIT_MODULES, KIT_STYLES, SVELTE_MODULES } from "./web/runtimeModules";

const WEBVIEW = path.join(__dirname, "..", "webview-ui", "node_modules");
const readJson = (p: string) => JSON.parse(fs.readFileSync(p, "utf8"));
const HAS_DEPS = fs.existsSync(path.join(WEBVIEW, "svelte", "package.json"));

type Exports = Record<string, string | Record<string, string>>;

test("the runtime has every client entry svelte exports", { skip: !HAS_DEPS && "webview-ui deps not installed" }, () => {
  const exports: Exports = readJson(path.join(WEBVIEW, "svelte", "package.json")).exports;
  const leftOut = new Set(["./package.json", "./compiler", "./server", "./internal/server", "./internal"]);
  const expected = Object.entries(exports)
    .filter(([k, v]) => !leftOut.has(k) && typeof v === "object" && Object.keys(v).some((c) => c !== "types"))
    .map(([k]) => (k === "." ? "svelte" : "svelte/" + k.slice(2)))
    .sort();
  assert.deepEqual([...SVELTE_MODULES].sort(), expected);
});

test("the runtime has every entry the kit exports", { skip: !HAS_DEPS && "webview-ui deps not installed" }, () => {
  const kit = readJson(path.join(WEBVIEW, "@joyautomation", "nautilus-hmi", "package.json"));
  const name = (k: string) => (k === "." ? kit.name : `${kit.name}/${k.slice(2)}`);
  const keys = Object.keys(kit.exports as Exports).filter((k) => k !== "./package.json");
  assert.deepEqual([...KIT_MODULES].sort(), keys.filter((k) => !k.endsWith(".css")).map(name).sort());
  assert.deepEqual([...KIT_STYLES].sort(), keys.filter((k) => k.endsWith(".css")).map(name).sort());
});

test("the compiler the tests use is the one the playground ships", { skip: !HAS_DEPS && "webview-ui deps not installed" }, () => {
  // esbuild.web.mjs bundles webview-ui's svelte/compiler (and the runtime
  // is webview-ui's svelte); componentCompiler.test.ts compiles with this
  // package's. Pinned alike, so the tests compile what visitors get.
  const ours = readJson(path.join(__dirname, "..", "node_modules", "svelte", "package.json")).version;
  const webview = readJson(path.join(WEBVIEW, "svelte", "package.json")).version;
  assert.equal(ours, webview);
});
