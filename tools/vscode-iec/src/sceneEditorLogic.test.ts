import { strict as assert } from "node:assert";
import { test } from "node:test";
import * as path from "node:path";
import { locateAsset, resolveSceneAssets, sceneAssets } from "./sceneEditorLogic";

const DOC = {
  kinds: {
    pump: { model: "models/pump.glb" },
    skid: { component: "hmi/src/lib/Skid.svelte" },
    tank: { model: "models/tank.glb", drive: [] },
  },
  environment: { hdri: "env/a.hdr", backdrop: "env/b.jpg", background: "ground" },
  fixtures: [
    { kind: "box", pos: [0, 0, 0] },
    { kind: "plane", pos: [0, 0, 0], texture: { map: "textures/floor.jpg", normalMap: "textures/floor_nor.jpg" } },
  ],
  nodes: [],
};

test("sceneAssets lists models, the environment's images and fixture textures, with pointers", () => {
  assert.deepEqual(sceneAssets(DOC), [
    ["/kinds/pump/model", "models/pump.glb"],
    ["/kinds/tank/model", "models/tank.glb"],
    ["/environment/hdri", "env/a.hdr"],
    ["/environment/backdrop", "env/b.jpg"],
    ["/fixtures/1/texture/map", "textures/floor.jpg"],
    ["/fixtures/1/texture/normalMap", "textures/floor_nor.jpg"],
  ]);
  assert.deepEqual(sceneAssets(null), []);
  assert.deepEqual(sceneAssets({ kinds: { a: "nope" } }), []);
});

test("locateAsset tries the scene's directory, then the HMI app's static/ and build/", () => {
  const dir = path.join(path.sep, "proj");
  const have = new Set([path.join(dir, "hmi", "static", "models", "pump.glb"), path.join(dir, "env", "a.hdr")]);
  const exists = (f: string) => have.has(f);
  assert.equal(locateAsset("models/pump.glb", dir, exists), path.join(dir, "hmi", "static", "models", "pump.glb"));
  assert.equal(locateAsset("env/a.hdr", dir, exists), path.join(dir, "env", "a.hdr"));
  assert.equal(locateAsset("/env/a.hdr", dir, exists), path.join(dir, "env", "a.hdr"), "a leading slash is a URL path, not an absolute file");
  assert.equal(locateAsset("missing.glb", dir, exists), undefined);
  assert.equal(locateAsset("../../etc/passwd", dir, () => true), undefined, "never climbs out");
  assert.equal(locateAsset("/etc/passwd", dir, () => true), path.join(dir, "etc", "passwd"), "a leading slash is the app root, never the filesystem root");
});

test("resolveSceneAssets rewrites what exists, leaves the rest, and reports both", () => {
  const dir = path.join(path.sep, "proj");
  const pump = path.join(dir, "hmi", "static", "models", "pump.glb");
  const exists = (f: string) => f === pump;
  const { doc, assets } = resolveSceneAssets(DOC, dir, exists, (f) => "vscode-resource://" + f);
  const d = doc as typeof DOC;
  assert.equal(d.kinds.pump.model, "vscode-resource://" + pump);
  assert.equal(d.kinds.tank.model, "models/tank.glb", "a missing model keeps its path");
  assert.equal(d.environment.hdri, "env/a.hdr");
  assert.equal(DOC.kinds.pump.model, "models/pump.glb", "the input is not mutated");
  assert.deepEqual(assets[0], { pointer: "/kinds/pump/model", path: "models/pump.glb", file: pump });
  assert.deepEqual(assets[1], { pointer: "/kinds/tank/model", path: "models/tank.glb" });
  assert.equal(assets.length, 6);
});
