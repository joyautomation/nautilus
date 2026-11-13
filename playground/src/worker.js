// The playground's engine room: one Web Worker holding naut.wasm and the
// in-memory project it reads. The page talks to it only through the
// messages below (client.js wraps them); nothing here touches the network
// after naut.wasm itself is fetched.
//
// page -> worker   {id, op, args}
// worker -> page   {id, ok, result} | {id, ok: false, error}
//                  {event: 'ready', version, timing}
//                  {event: 'stream', sid, chunk}     chunk null = ended
//                  {event: 'lsp', msg}               one JSON-RPC message
//                  {event: 'fs', path, kind}         Go changed a file
//                  {event: 'log', fd, text}          Go's stdout/stderr
// Assets resolve against the worker's own URL, or against NAUTILUS_BASE when
// a host starts it from a blob: (VS Code's web extension host does).
const BASE = self.NAUTILUS_BASE || self.location.href;
const vq = self.NAUTILUS_BASE ? '' : new URL(self.location.href).search; // ?v=<version>, passed on to every asset
importScripts(new URL('memfs.js' + vq, BASE).href);

const mem = self.createMemFS({ log: (fd, text) => postMessage({ event: 'log', fd, text }) });
self.fs = mem.fs;
self.process = mem.process;
self.path = mem.path;
mem.api.onChange((path, kind) => postMessage({ event: 'fs', path, kind }));

importScripts(new URL('wasm_exec.js' + vq, BASE).href);

const T = { start: performance.now() };
let markReady;
const ready = new Promise((r) => (markReady = r));
// The packages naut builds a screen with (Svelte, the HMI kits, three,
// Threlte): not in naut.wasm, which fetches them the first time a project
// has a screen to build (internal/hmibuild/packs_js.go).
self.nautilusHMIPack = () =>
	fetch(new URL('hmi-packs.tgz' + vq, BASE)).then((r) => {
		if (!r.ok) throw new Error('hmi-packs.tgz: ' + r.status);
		return r.arrayBuffer();
	}).then((b) => new Uint8Array(b));

self.onNautilusReady = () => {
	T.goReady = performance.now();
	markReady();
};

(async () => {
	const go = new Go();
	// The page starts this worker as worker.js?v=<version>; the same tag on
	// naut.wasm lets both be cached for good and still change together.
	const url = new URL('naut.wasm' + vq, BASE);
	let inst;
	if (WebAssembly.instantiateStreaming) {
		try {
			({ instance: inst } = await WebAssembly.instantiateStreaming(fetch(url), go.importObject));
		} catch (e) {
			// A server without the application/wasm type: compile from bytes.
			inst = null;
		}
	}
	if (!inst) {
		const bytes = await (await fetch(url)).arrayBuffer();
		({ instance: inst } = await WebAssembly.instantiate(bytes, go.importObject));
	}
	T.instantiated = performance.now();
	go.run(inst).then(() => postMessage({ event: 'exit' }));
	await ready;
	postMessage({ event: 'ready', version: self.nautilus.version, timing: T });
})().catch((e) => postMessage({ event: 'fatal', error: String((e && e.stack) || e) }));

const streams = new Map();
let lspSend = null;

const ops = {
	// files
	writeFile: (p, text) => mem.api.writeFile(p, text),
	readFile: (p) => mem.api.readFile(p),
	remove: (p) => mem.api.remove(p),
	snapshot: (root) => mem.api.snapshot(root),
	// The file-system calls a host needs to show the project as a folder
	// (the playground app's file:// provider). Same errors as Node's.
	fsCall: (name, ...args) =>
		new Promise((resolve, reject) => {
			const cb = (err, v) => (err ? reject(Object.assign(new Error(err.message), { code: err.code })) : resolve(v));
			if (name === 'stat') return mem.fs.stat(args[0], (e, st) => cb(e, st && { dir: st.isDirectory(), size: st.size, mtime: st.mtimeMs, ctime: st.ctimeMs }));
			if (name === 'readFile') {
				const t = mem.api.readFile(args[0]);
				return t === null ? cb({ code: 'ENOENT', message: 'ENOENT: ' + args[0] }) : cb(null, t);
			}
			if (name === 'writeFile') {
				mem.api.writeFile(args[0], args[1]);
				return cb(null);
			}
			mem.fs[name](...args, cb);
		}),
	/** Replace everything under root with files ({relativePath: text}). */
	resetProject(root, files) {
		mem.api.remove(root);
		mem.api.mkdirp(root);
		for (const [p, text] of Object.entries(files)) mem.api.writeFile(root + '/' + p, text);
		self.process.chdir(root);
	},

	// the controller
	load: (root) => self.nautilus.load(root),
	stop: () => self.nautilus.stop(),
	stats: () => self.nautilus.stats(),
	handle: (method, url, headers, body) => self.nautilus.handle(method, url, headers || {}, body == null ? null : body),
	// the project's own screen, which the engine builds (sw.js view/app/)
	app: (method, url, headers, body) => self.nautilus.app(method, url, headers || {}, body == null ? null : body),
	appStatus: () => self.nautilus.appStatus(),
	stream(sid, url) {
		const cancel = self.nautilus.stream(url, (chunk) => {
			postMessage({ event: 'stream', sid, chunk });
			if (chunk === null) streams.delete(sid);
		});
		streams.set(sid, cancel);
	},
	streamCancel(sid) {
		const cancel = streams.get(sid);
		if (cancel) cancel();
		streams.delete(sid);
	},

	// diagram editors, language server, tests
	exec: (args, stdin, cwd) => self.nautilus.exec(args, stdin == null ? '' : stdin, cwd || ''),
	graph: (lang, src, at) => JSON.parse(self.nautilus.graph(lang, src, at || '')),
	edit: (lang, request) => JSON.parse(self.nautilus.edit(lang, JSON.stringify(request))),
	lspStart() {
		if (!lspSend) lspSend = self.nautilus.lsp((msg) => postMessage({ event: 'lsp', msg: JSON.parse(msg) }));
	},
	lspSend(msg) {
		lspSend(JSON.stringify(msg));
	},
	test: (root, pattern) => self.nautilus.test(root, pattern || ''),
};

onmessage = async (e) => {
	const { id, op, args } = e.data;
	try {
		await ready;
		if (!(op in ops)) throw new Error('unknown op ' + op);
		const result = await ops[op](...(args || []));
		if (id != null) postMessage({ id, ok: true, result });
	} catch (err) {
		if (id != null) postMessage({ id, ok: false, error: String((err && err.message) || err) });
	}
};
