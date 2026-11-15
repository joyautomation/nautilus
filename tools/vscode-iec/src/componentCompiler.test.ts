// The browser's user-components build (web/componentCompiler.ts), run in
// Node: the same compile and link the playground does in its extension
// host. The linked script is then evaluated against Svelte's real client
// runtime, as the mimic editor's webview would after user-runtime.js.

import { strict as assert } from "node:assert";
import { test } from "node:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { compileComponents, type ReadFile } from "./web/componentCompiler";
import { RUNTIME_MODULES } from "./web/runtimeModules";
import { isScenePart, sceneHostPath, sceneHostSource } from "./componentPreviewLogic";

const FIXTURES = path.join(__dirname, "..", "src", "testdata", "user-components");
const LIFT = path.join(__dirname, "..", "..", "..", "examples", "lift-station", "hmi", "src", "lib");

const disk: ReadFile = async (p) => {
  try {
    return await fs.promises.readFile(p, "utf8");
  } catch {
    return undefined;
  }
};

/** A project held in memory: absolute path -> text. */
const memory =
  (files: Record<string, string>): ReadFile =>
  async (p) =>
    files[p];

type Registered = Record<string, { mount: unknown }>;

/** Runs a linked script the way the webview does, with Svelte's client
 * runtime (and an empty kit) as window.__NX_RT__. */
async function run(
  code: string,
  override: Record<string, unknown> = {},
  extra: Record<string, unknown> = {}
): Promise<{ registered: Registered | undefined; events: string[]; errors: unknown[] }> {
  const rt: Record<string, unknown> = {};
  for (const spec of RUNTIME_MODULES) {
    rt[spec] = spec.startsWith("svelte") ? await import(spec === "svelte" ? "svelte/internal/client" : spec).catch(() => ({})) : {};
  }
  // `svelte` itself: mount/unmount, from the client build (Node would pick the server one).
  rt.svelte = await import(path.join(path.dirname(require.resolve("svelte/package.json")), "src", "index-client.js"));
  Object.assign(rt, override);
  const events: string[] = [];
  const errors: unknown[] = [];
  const window: Record<string, unknown> = { ...extra, __NX_RT__: rt, dispatchEvent: (e: { type: string }) => events.push(e.type) };
  const console = { error: (...a: unknown[]) => errors.push(a) };
  new Function("window", "console", "document", "Event", code)(window, console, {}, class {
    constructor(public type: string) {}
  });
  return { registered: window.__NX_USER_COMPONENTS__ as Registered | undefined, events, errors };
}

test("compiles a component with a TypeScript script, and the script registers it", async () => {
  const result = await compileComponents({ Supply: path.join(FIXTURES, "Supply.svelte") }, disk);
  assert.deepEqual(result.diagnostics, []);
  assert.deepEqual(result.built, ["Supply"]);
  const { registered, events, errors } = await run(result.code);
  assert.deepEqual(errors, []);
  assert.deepEqual(events, ["nx:user-components-ready"]);
  assert.equal(typeof registered?.Supply?.mount, "function");
});

test("lift-station's own components compile; the kit comes from the runtime", async () => {
  const names = ["FloatSwitch", "SeqStepTag", "SubmersiblePump", "ToProcess"];
  const result = await compileComponents(Object.fromEntries(names.map((n) => [n, path.join(LIFT, `${n}.svelte`)])), disk);
  assert.deepEqual(result.diagnostics, []);
  assert.deepEqual(result.built, names);
  assert.match(result.code, /"rt:@joyautomation\/nautilus-hmi"/);
  const { registered, errors } = await run(result.code);
  assert.deepEqual(errors, []);
  assert.deepEqual(Object.keys(registered ?? {}).sort(), names);
});

test("relative, $lib and TypeScript helper imports resolve through the project", async () => {
  const files = {
    "/p/hmi/svelte.config.js": "export default {};",
    "/p/hmi/src/lib/units.ts": "export const unit: string = 'kPa';\nexport type Unit = string;",
    "/p/hmi/src/lib/data.json": '{ "max": 10 }',
    "/p/hmi/src/lib/parts/Label.svelte": "<script lang=\"ts\">let { text }: { text: string } = $props();</script><span>{text}</span>",
    "/p/hmi/src/lib/Gauge.svelte":
      '<script lang="ts">import Label from "./parts/Label.svelte"; import { unit } from "$lib/units"; import data from "./data.json";' +
      "let { v = 0 }: { v?: number } = $props();</script><Label text={`${v}/${data.max} ${unit}`} />",
  };
  const result = await compileComponents({ Gauge: "/p/hmi/src/lib/Gauge.svelte" }, memory(files));
  assert.deepEqual(result.diagnostics, []);
  for (const f of ["/p/hmi/src/lib/units.ts", "/p/hmi/src/lib/parts/Label.svelte", "/p/hmi/src/lib/data.json"]) {
    assert.ok(result.code.includes(JSON.stringify(f)), f);
  }
  const { registered, errors } = await run(result.code);
  assert.deepEqual(errors, []);
  assert.equal(typeof registered?.Gauge?.mount, "function");
});

test("a component that fails is left out with a reason; the rest still build", async () => {
  const files = {
    "/p/Good.svelte": "<p>ok</p>",
    "/p/Broken.svelte": "<p>{oops</p>",
    "/p/Charts.svelte": '<script>import Chart from "chart.js";</script>',
    "/p/Missing.svelte": '<script>import X from "./Nope.svelte";</script>',
  };
  const result = await compileComponents(
    { Good: "/p/Good.svelte", Broken: "/p/Broken.svelte", Charts: "/p/Charts.svelte", Missing: "/p/Missing.svelte", Gone: "/p/Gone.svelte" },
    memory(files)
  );
  assert.deepEqual(result.built, ["Good"]);
  const why = Object.fromEntries(result.diagnostics.map((d) => [d.component, d.message]));
  assert.deepEqual(Object.keys(why).sort(), ["Broken", "Charts", "Gone", "Missing"]);
  assert.match(why.Charts, /"chart\.js", which the playground cannot load/);
  assert.match(why.Broken, /^\/p\/Broken\.svelte:1:\d+: /, "a compile error says where");
  assert.match(why.Missing, /"\.\/Nope\.svelte", which is not in the project/);
  assert.match(why.Gone, /not in the project/);
  assert.equal(typeof (await run(result.code)).registered?.Good?.mount, "function");
});

test("a prop named `state` beside $state(...) is stopped at compile, with where and why", async () => {
  // Svelte reads `$state` as a store subscription to the `state` prop, and
  // the component throws when it mounts ("e.subscribe is not a function"),
  // blanking the islands after it (the bw01 chart, 11-13).
  const files = {
    "/p/Cell.svelte": "<script>\n  let { state = 1 } = $props();\n  let error = $state('');\n</script>\n<p>{state}{error}</p>",
    "/p/Fine.svelte": "<script>\n  let { mode = 1 } = $props();\n  let error = $state('');\n</script>\n<p>{mode}{error}</p>",
  };
  const result = await compileComponents({ Cell: "/p/Cell.svelte", Fine: "/p/Fine.svelte" }, memory(files));
  assert.deepEqual(result.built, ["Fine"]);
  const [d] = result.diagnostics;
  assert.equal(d.component, "Cell");
  assert.match(d.message, /^\/p\/Cell\.svelte:\d+:\d+: .*store_rune_conflict: it throws when it mounts/);
});

test("an editor's unsaved text is what compiles", async () => {
  const result = await compileComponents({ A: "/p/A.svelte" }, memory({ "/p/A.svelte": "<p>on-disk</p>" }), {
    sources: { "/p/A.svelte": "<p>in-the-editor</p>" },
  });
  assert.match(result.code, /in-the-editor/);
  assert.doesNotMatch(result.code, /on-disk/);
});

test("nothing usable: the script still registers (nothing) and says it is ready", async () => {
  const result = await compileComponents({ X: "/nope.svelte" }, memory({}));
  assert.deepEqual(result.built, []);
  const { registered, events } = await run(result.code);
  assert.deepEqual(registered, {});
  assert.deepEqual(events, ["nx:user-components-ready"]);
});

test("a runtime export assigned after the script loads is read live", async () => {
  // Svelte sets $.window on the first mount: a copy of the runtime module
  // taken when the script loaded left <svelte:window> with undefined.
  let late: string | undefined;
  const store = Object.freeze({
    get late() {
      return late;
    },
  });
  const files = {
    "/p/probe.js": 'import * as store from "svelte/store"; window.probe.read = () => store.late;',
    "/p/A.svelte": '<script>import "./probe.js";</script><p>a</p>',
  };
  const result = await compileComponents({ A: "/p/A.svelte" }, memory(files));
  assert.deepEqual(result.diagnostics, []);
  const probe: { read?: () => unknown } = {};
  const { errors } = await run(result.code, { "svelte/store": store }, { probe });
  assert.deepEqual(errors, []);
  assert.equal(probe.read?.(), undefined);
  late = "set";
  assert.equal(probe.read?.(), "set");
});

test("a project barrel can export * from a runtime module", async () => {
  const store = Object.freeze({ writable: () => "w", readable: () => "r" });
  const files = {
    "/p/barrel.js": 'export * from "svelte/store";',
    "/p/probe.js": 'import * as b from "./barrel.js"; window.probe.keys = Object.keys(b).sort(); window.probe.w = b.writable();',
    "/p/A.svelte": '<script>import "./probe.js";</script><p>a</p>',
  };
  const result = await compileComponents({ A: "/p/A.svelte" }, memory(files));
  assert.deepEqual(result.diagnostics, []);
  const probe: { keys?: string[]; w?: string } = {};
  const { errors } = await run(result.code, { "svelte/store": store }, { probe });
  assert.deepEqual(errors, []);
  assert.deepEqual(probe.keys, ["readable", "writable"]);
  assert.equal(probe.w, "w");
});

test("a part of a 3D scene compiles inside its host, on the 3D runtime", async () => {
  const rain = path.join(LIFT, "Rain.svelte");
  assert.equal(isScenePart(fs.readFileSync(rain, "utf8")), true);
  const host = sceneHostPath(rain);
  const result = await compileComponents({ Rain: host }, disk, { sources: { [host]: sceneHostSource(rain) } });
  assert.deepEqual(result.diagnostics, []);
  assert.deepEqual(result.built, ["Rain"]);
  for (const spec of ["three", "@threlte/core", "three/examples/jsm/controls/OrbitControls.js"]) {
    assert.ok(result.code.includes(JSON.stringify("rt:" + spec)), spec);
  }
  assert.ok(result.code.includes(JSON.stringify(rain)), "the part itself is in the bundle");
});
