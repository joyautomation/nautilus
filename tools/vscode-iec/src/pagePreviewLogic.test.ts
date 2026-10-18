import { test } from "node:test";
import * as assert from "node:assert/strict";
import * as path from "path";
import {
  svelteKitRootFor,
  routeForFile,
  routeHasParams,
  devServerArgs,
  pageUrl,
  nautilusProjectAbove,
} from "./pagePreviewLogic";

const existsIn = (files: string[]) => (p: string) => files.includes(p);
const P = (...s: string[]) => path.resolve("/", ...s);

test("svelteKitRootFor walks up to the nearest svelte.config.*", () => {
  const ex = existsIn([P("plant", "hmi", "svelte.config.js")]);
  assert.equal(svelteKitRootFor(P("plant", "hmi", "src", "routes", "+page.svelte"), ex), P("plant", "hmi"));
  assert.equal(svelteKitRootFor(P("plant", "hmi", "src", "routes", "pumps", "+page.svelte"), ex), P("plant", "hmi"));
  assert.equal(svelteKitRootFor(P("plant", "sim.st"), ex), null);
});

test("svelteKitRootFor accepts the .ts / .mjs config spellings", () => {
  assert.equal(svelteKitRootFor(P("a", "src", "x.svelte"), existsIn([P("a", "svelte.config.ts")])), P("a"));
  assert.equal(svelteKitRootFor(P("a", "src", "x.svelte"), existsIn([P("a", "svelte.config.mjs")])), P("a"));
});

test("routeForFile maps a routes file to its route", () => {
  const root = P("plant", "hmi");
  const r = (...s: string[]) => routeForFile(P("plant", "hmi", "src", "routes", ...s), root);
  assert.equal(r("+page.svelte"), "/");
  assert.equal(r("+layout.svelte"), "/");
  assert.equal(r("pumps", "+page.svelte"), "/pumps");
  assert.equal(r("pumps", "p101", "+page.svelte"), "/pumps/p101");
  assert.equal(r("pumps", "Faceplate.svelte"), "/pumps", "a non-+ file previews the route it sits in");
});

test("routeForFile drops (group) segments and keeps [params] as written", () => {
  const root = P("hmi");
  assert.equal(routeForFile(P("hmi", "src", "routes", "(app)", "trends", "+page.svelte"), root), "/trends");
  assert.equal(routeForFile(P("hmi", "src", "routes", "pumps", "[id]", "+page.svelte"), root), "/pumps/[id]");
  assert.equal(routeHasParams("/pumps/[id]"), true);
  assert.equal(routeHasParams("/pumps"), false);
});

test("routeForFile is null outside src/routes", () => {
  const root = P("hmi");
  assert.equal(routeForFile(P("hmi", "src", "lib", "Pump.svelte"), root), null);
  assert.equal(routeForFile(P("other", "src", "routes", "+page.svelte"), root), null);
});

test("devServerArgs pins the port strictly; pageUrl uses localhost", () => {
  assert.deepEqual(devServerArgs(5181), ["run", "dev", "--", "--port", "5181", "--strictPort"]);
  assert.equal(pageUrl(5181, "/"), "http://localhost:5181/");
  assert.equal(pageUrl(5181, "/pumps"), "http://localhost:5181/pumps");
});

test("nautilusProjectAbove finds the manifest above the hmi folder", () => {
  const ex = existsIn([P("plant", "nautilus.yaml")]);
  assert.equal(nautilusProjectAbove(P("plant", "hmi"), ex), P("plant"));
  assert.equal(nautilusProjectAbove(P("elsewhere"), ex), null);
});
