// The page's handle on the playground engine (worker.js + naut.wasm).
//
//   const pg = await Playground.start({ base: '/playground/0.1.0/' });
//   await pg.setProject({ 'nautilus.yaml': '...', 'program.ld': '...' });
//   const { ok, error, notices } = await pg.run();
//   const state = await pg.api('GET', '/api/state');
//   const stop = pg.stream('/api/stream?delta=1', (ev) => ...);
//
// Every method that reaches Go is async; the worker runs the scan loop, so
// the page's main thread only ever renders.

export const ROOT = '/project';

export class Playground {
	/**
	 * Start the worker and wait for naut.wasm to be ready.
	 * @param {{ base: string, version?: string }} opts  base: URL of the
	 *   directory holding worker.js, memfs.js, wasm_exec.js and naut.wasm;
	 *   version, when given, tags the worker and wasm URLs for caching.
	 */
	static async start(opts) {
		const base = new URL(opts.base, location.href);
		const worker = new Worker(new URL('worker.js' + (opts.version ? '?v=' + encodeURIComponent(opts.version) : ''), base));
		const pg = new Playground(worker);
		await pg.ready;
		return pg;
	}

	constructor(worker) {
		this.worker = worker;
		this.seq = 0;
		this.pending = new Map();
		this.streams = new Map();
		this.listeners = { lsp: new Set(), fs: new Set(), log: new Set(), exit: new Set() };
		this.version = '';
		this.timing = null;
		this.ready = new Promise((resolve, reject) => {
			this._ready = resolve;
			this._fatal = reject;
		});
		worker.onmessage = (e) => this._onMessage(e.data);
		worker.onerror = (e) => this._fatal(new Error(e.message || 'playground worker failed to start'));
	}

	_onMessage(m) {
		if (m.event) {
			switch (m.event) {
				case 'ready':
					this.version = m.version;
					this.timing = m.timing;
					this._ready(this);
					return;
				case 'fatal':
					this._fatal(new Error(m.error));
					return;
				case 'stream': {
					const s = this.streams.get(m.sid);
					if (s) s(m.chunk);
					if (m.chunk === null) this.streams.delete(m.sid);
					return;
				}
				default:
					for (const fn of this.listeners[m.event] || []) fn(m);
					return;
			}
		}
		const p = this.pending.get(m.id);
		if (!p) return;
		this.pending.delete(m.id);
		m.ok ? p.resolve(m.result) : p.reject(new Error(m.error));
	}

	/** Low-level RPC into the worker. */
	call(op, ...args) {
		return new Promise((resolve, reject) => {
			const id = ++this.seq;
			this.pending.set(id, { resolve, reject });
			this.worker.postMessage({ id, op, args });
		});
	}

	/** Subscribe to 'lsp' | 'fs' | 'log' | 'exit' events; returns unsubscribe. */
	on(event, fn) {
		this.listeners[event].add(fn);
		return () => this.listeners[event].delete(fn);
	}

	// ---- the project's files (under ROOT in the worker's file system)

	/** Replace the whole project. files: {relativePath: text}. */
	setProject(files) {
		return this.call('resetProject', ROOT, files);
	}
	writeFile(rel, text) {
		return this.call('writeFile', ROOT + '/' + rel, text);
	}
	removeFile(rel) {
		return this.call('remove', ROOT + '/' + rel);
	}
	/** Every project file, {relativePath: text}. */
	files() {
		return this.call('snapshot', ROOT);
	}

	// ---- the controller

	/** Compile the project as it stands and start scanning (replacing a running one). */
	run() {
		return this.call('load', ROOT);
	}
	stop() {
		return this.call('stop');
	}
	stats() {
		return this.call('stats');
	}

	/** One tag-API request; the body is parsed as JSON when it is JSON. */
	async api(method, url, body) {
		const r = await this.call('handle', method, url, { 'Content-Type': 'application/json' }, body === undefined ? null : JSON.stringify(body));
		let json = r.body;
		try {
			json = JSON.parse(r.body);
		} catch {
			/* not JSON: keep the text */
		}
		return { status: r.status, headers: r.headers, body: json };
	}

	/** A fetch()-compatible function for code written against the tag API over HTTP. */
	get fetch() {
		return async (input, init = {}) => {
			const url = typeof input === 'string' ? input : input.url;
			const u = new URL(url, 'http://playground');
			const headers = {};
			new Headers(init.headers || {}).forEach((v, k) => (headers[k] = v));
			const r = await this.call('handle', (init.method || 'GET').toUpperCase(), u.pathname + u.search, headers, init.body == null ? null : String(init.body));
			return new Response(r.body, { status: r.status, headers: r.headers });
		};
	}

	/**
	 * A streaming endpoint's raw text, chunk by chunk as the server flushes
	 * it; onChunk(null) when it ends. Returns the function that stops it.
	 */
	streamRaw(url, onChunk) {
		const sid = ++this.seq;
		this.streams.set(sid, onChunk);
		this.worker.postMessage({ op: 'stream', args: [sid, url] });
		return () => {
			this.streams.delete(sid);
			this.worker.postMessage({ op: 'streamCancel', args: [sid] });
		};
	}

	/**
	 * Server-sent events from a streaming endpoint (GET /api/stream...),
	 * parsed: onEvent({event, data}) per event, onEvent(null) when it ends.
	 * Returns the function that stops it.
	 */
	stream(url, onEvent) {
		const sid = ++this.seq;
		let buf = '';
		this.streams.set(sid, (chunk) => {
			if (chunk === null) {
				onEvent(null);
				return;
			}
			buf += chunk;
			let i;
			while ((i = buf.indexOf('\n\n')) !== -1) {
				const block = buf.slice(0, i);
				buf = buf.slice(i + 2);
				let event = 'message';
				const data = [];
				for (const line of block.split('\n')) {
					if (line.startsWith(':')) continue;
					const c = line.indexOf(':');
					const field = c === -1 ? line : line.slice(0, c);
					const value = c === -1 ? '' : line.slice(c + 1).replace(/^ /, '');
					if (field === 'event') event = value;
					else if (field === 'data') data.push(value);
				}
				if (data.length) onEvent({ event, data: data.join('\n') });
			}
		});
		this.worker.postMessage({ op: 'stream', args: [sid, url] });
		return () => {
			this.streams.delete(sid);
			this.worker.postMessage({ op: 'streamCancel', args: [sid] });
		};
	}

	// ---- editors, language server, tests

	/** A `naut` verb the VS Code extension runs (compose, ld|fbd|sfc graph|edit, test -json|-list). */
	exec(args, stdin, cwd) {
		return this.call('exec', args, stdin, cwd);
	}

	/** `naut <lang> graph` for a diagram source; at is its project-relative path. */
	graph(lang, src, at) {
		return this.call('graph', lang, src, at ? ROOT + '/' + at : '');
	}
	/** `naut <lang> edit`; returns {out, code} as the CLI would. */
	edit(lang, request) {
		const req = { ...request };
		if (req.file) req.file = ROOT + '/' + req.file;
		return this.call('edit', lang, req);
	}
	/** Start the language server; messages arrive on on('lsp'). */
	lspStart() {
		return this.call('lspStart');
	}
	lspSend(msg) {
		this.worker.postMessage({ op: 'lspSend', args: [msg] });
	}
	/**
	 * `naut test --json`, optionally only tests matching pattern: one record
	 * per line on the CLI, an array here. A project that cannot run its
	 * tests (no suites, a compile error) answers [{error}].
	 */
	async test(pattern) {
		const text = await this.call('test', ROOT, pattern || '');
		return text.split('\n').filter((l) => l.trim()).map((l) => JSON.parse(l));
	}

	terminate() {
		this.worker.terminate();
	}
}

/** The file:// URI the language server knows a project file by. */
export function fileUri(rel) {
	return 'file://' + ROOT + '/' + rel;
}
