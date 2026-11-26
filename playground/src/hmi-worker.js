// The screen builder's room: a Web Worker holding naut-hmi.wasm
// (cmd/naut-wasm-hmi: esbuild and the Svelte pipeline) and a copy of the
// project's files. worker.js starts it the first time a screen is asked for
// (nautilusHMIBuild there), so a visit that never opens a screen never
// downloads the 2 MB module, and a build never stalls the scan loop, which
// runs in worker.js's thread.
//
// worker.js -> here  {id, root, files: {relativePath: Uint8Array}, opts}
// here -> worker.js  {id, ok: true, files, pages, ms} | {id, ok: false, messages}
//                    {event: 'ready', timing}   the module is compiled and running
//                    {event: 'fatal', error}    it could not be fetched or started
const BASE = self.NAUTILUS_BASE || self.location.href;
const vq = self.NAUTILUS_BASE ? '' : new URL(self.location.href).search; // ?v=<version>, as worker.js passes it
importScripts(new URL('memfs.js' + vq, BASE).href);

const mem = self.createMemFS({ log: () => {} });
self.fs = mem.fs;
self.process = mem.process;
self.path = mem.path;

importScripts(new URL('wasm_exec.js' + vq, BASE).href);

const T = { start: performance.now() };
let markReady;
let markFailed;
const ready = new Promise((r, j) => ((markReady = r), (markFailed = j)));
ready.catch(() => {}); // reported once, to the page, by 'fatal'

// The packages the build uses (Svelte, the HMI kits, three, Threlte).
self.nautilusHMIPack = () =>
	fetch(new URL('hmi-packs.tgz' + vq, BASE)).then((r) => {
		if (!r.ok) throw new Error('hmi-packs.tgz: ' + r.status);
		return r.arrayBuffer();
	}).then((b) => new Uint8Array(b));

self.onNautilusHMIReady = () => {
	T.goReady = performance.now();
	markReady();
};

(async () => {
	const go = new Go();
	const url = new URL('naut-hmi.wasm' + vq, BASE);
	let inst;
	if (WebAssembly.instantiateStreaming) {
		try {
			const res = await fetch(url);
			if (!res.ok) throw new Error('naut-hmi.wasm: ' + res.status);
			T.fetched = performance.now();
			({ instance: inst } = await WebAssembly.instantiateStreaming(res, go.importObject));
		} catch (e) {
			// A server without the application/wasm type, or a failed fetch (the
			// second attempt below says which).
			inst = null;
		}
	}
	if (!inst) {
		const r = await fetch(url);
		if (!r.ok) throw new Error('naut-hmi.wasm: ' + r.status);
		const bytes = await r.arrayBuffer();
		T.fetched = performance.now();
		({ instance: inst } = await WebAssembly.instantiate(bytes, go.importObject));
	}
	T.instantiated = performance.now();
	go.run(inst).then(() => {
		markFailed(new Error('naut-hmi.wasm exited'));
		postMessage({ event: 'fatal', error: 'naut-hmi.wasm exited' }); // worker.js drops this worker
	});
	await ready;
	postMessage({ event: 'ready', timing: T });
})().catch((e) => {
	markFailed(e);
	postMessage({ event: 'fatal', error: String((e && e.message) || e) });
});

// One build at a time: each rewrites the project's files and the working directory,
// so a build started while another runs (Restart) waits its turn.
let turn = Promise.resolve();
onmessage = (e) => {
	turn = turn.then(() => build(e.data));
};

async function build({ id, root, files, opts }) {
	try {
		await ready;
		mem.api.remove(root);
		mem.api.mkdirp(root);
		for (const [p, bytes] of Object.entries(files)) mem.api.writeFile(root + '/' + p, bytes);
		self.process.chdir(root);
		const r = await self.nautilusHMI.build(root, opts);
		if (!r.ok) return postMessage({ id, ok: false, messages: r.messages });
		const buffers = Object.values(r.files).map((u8) => u8.buffer);
		postMessage({ id, ok: true, files: r.files, pages: r.pages, ms: r.ms }, buffers);
	} catch (err) {
		postMessage({ id, ok: false, messages: [String((err && err.message) || err)] });
	}
}
