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
importScripts('memfs.js');

const mem = self.createMemFS({ log: (fd, text) => postMessage({ event: 'log', fd, text }) });
self.fs = mem.fs;
self.process = mem.process;
self.path = mem.path;
mem.api.onChange((path, kind) => postMessage({ event: 'fs', path, kind }));

importScripts('wasm_exec.js');

const T = { start: performance.now() };
let markReady;
const ready = new Promise((r) => (markReady = r));
self.onNautilusReady = () => {
	T.goReady = performance.now();
	markReady();
};

(async () => {
	const go = new Go();
	const url = new URL('naut.wasm', self.location.href);
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
