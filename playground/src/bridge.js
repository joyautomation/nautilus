// The page side of the engine bridge: lets a VS Code extension running in
// the workbench's extension-host worker (same origin, different thread) use
// this page's engine over a BroadcastChannel. The extension side is
// tools/vscode-iec/src/web/engine.ts.
//
// Each page names its own channel (two playground tabs must not answer each
// other), and hands the name to the extension in a setting.
//
//   ext -> page   {kind: 'call', id, op, args}       a Playground method
//                 {kind: 'stream', sid, url}          raw SSE text
//                 {kind: 'streamCancel', sid}
//                 {kind: 'lsp', msg}                  to the language server
//   page -> ext   {kind: 'result', id, ok, result|error}
//                 {kind: 'chunk', sid, chunk}         chunk null = ended
//                 {kind: 'lsp', msg}                  from the language server
//                 {kind: 'fs', path, kind}            a file Go changed

const CALLS = new Set(['exec', 'run', 'stop', 'stats', 'api', 'graph', 'edit', 'test', 'files', 'writeFile', 'removeFile', 'call']);

/** Serve pg on a new channel; returns its name and a stop function. */
export function serveBridge(pg, name = 'nautilus-engine-' + crypto.randomUUID()) {
	const ch = new BroadcastChannel(name);
	const streams = new Map();
	let lspStarted = false;
	const offLsp = pg.on('lsp', ({ msg }) => ch.postMessage({ kind: 'lsp', msg }));
	const offFs = pg.on('fs', ({ path, kind }) => ch.postMessage({ kind: 'fs', path, fsKind: kind }));
	ch.onmessage = async ({ data: m }) => {
		switch (m && m.kind) {
			case 'call': {
				try {
					if (!CALLS.has(m.op)) throw new Error('engine: no such call ' + m.op);
					const result = await pg[m.op](...(m.args || []));
					ch.postMessage({ kind: 'result', id: m.id, ok: true, result });
				} catch (e) {
					ch.postMessage({ kind: 'result', id: m.id, ok: false, error: String((e && e.message) || e) });
				}
				return;
			}
			case 'stream':
				streams.set(m.sid, pg.streamRaw(m.url, (chunk) => {
					ch.postMessage({ kind: 'chunk', sid: m.sid, chunk });
					if (chunk === null) streams.delete(m.sid);
				}));
				return;
			case 'streamCancel':
				streams.get(m.sid)?.();
				streams.delete(m.sid);
				return;
			case 'lsp':
				if (!lspStarted) {
					lspStarted = true;
					await pg.lspStart();
				}
				pg.lspSend(m.msg);
				return;
		}
	};
	return {
		name,
		stop() {
			for (const s of streams.values()) s();
			offLsp();
			offFs();
			ch.close();
		}
	};
}
