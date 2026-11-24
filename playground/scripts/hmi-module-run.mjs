// Runs naut-hmi.wasm in Node the way playground/src/hmi-worker.js runs it in
// a worker: the in-memory file system (memfs.js) as Go's globalThis.fs, the
// pack from a file for nautilusHMIPack, one project written into memory.
// Used by playground/app/hmi-module.test.mjs, in a child process of its own
// (it replaces Node's `process` global the way a worker's memfs does).
//
//   node hmi-module-run.mjs <naut-hmi.wasm> <wasm_exec.js> <memfs.js> <packs.tar.gz> <project.json>
//
// project.json is {root, opts, files: {relativePath: text | {b64: base64 bytes}}}; the
// result is printed as one JSON line: {ok, pages, files: [names], sha: {name: sha256
// of each built file}, ms} or {ok: false, messages}.
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { runInThisContext } from 'node:vm';

const [wasmPath, execPath, memfsPath, packsPath, projectPath] = process.argv.slice(2);
const stdout = process.stdout; // process is replaced below, as in a worker
const stderr = process.stderr;
const project = JSON.parse(readFileSync(projectPath, 'utf8'));
const wasm = readFileSync(wasmPath);
const packs = readFileSync(packsPath);

globalThis.self = globalThis;
runInThisContext(readFileSync(memfsPath, 'utf8'), { filename: memfsPath });
const mem = globalThis.createMemFS({ log: (fd, text) => stderr.write(text) });
const write = (s) => stdout.write(s);
globalThis.fs = mem.fs;
globalThis.process = mem.process;
globalThis.path = mem.path;
runInThisContext(readFileSync(execPath, 'utf8'), { filename: execPath });
globalThis.nautilusHMIPack = () => Promise.resolve(new Uint8Array(packs));

let markReady;
const ready = new Promise((r) => (markReady = r));
globalThis.onNautilusHMIReady = () => markReady();

const go = new Go();
const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
go.run(instance).then(() => write(JSON.stringify({ ok: false, messages: ['naut-hmi.wasm exited'] }) + '\n'));
await ready;

mem.api.mkdirp(project.root);
for (const [p, f] of Object.entries(project.files)) mem.api.writeFile(project.root + '/' + p, typeof f === 'string' ? f : new Uint8Array(Buffer.from(f.b64, 'base64')));
const r = await globalThis.nautilusHMI.build(project.root, JSON.stringify(project.opts));
write(JSON.stringify(r.ok ? { ok: true, pages: r.pages, files: Object.keys(r.files), sha: Object.fromEntries(Object.entries(r.files).map(([k, u8]) => [k, createHash('sha256').update(u8).digest('hex')])), ms: r.ms } : { ok: false, messages: r.messages }) + '\n');
