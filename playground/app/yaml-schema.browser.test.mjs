// The playground's YAML schemas, in a real (headless) Chrome over the built
// playground (run after scripts/build.sh):
//
//     node --test playground/app/yaml-schema.browser.test.mjs
//
// The YAML language server runs in the browser and fetches a schema over
// http(s) only. main.js points the nautilus extension's yamlValidation at
// the served schema files; without that, the server is handed an
// extension-file:// URI it cannot read ("Unable to load schema ... No
// content") and a project file gets no completion, hover or validation.
// So: open a project's nautilus.yaml, and its tag file (whose schema $refs
// nautilus.schema.json), and the status bar must name each schema.
//
// Chrome comes from the gesture harness's client (no npm dependencies);
// GESTURE_NO_SANDBOX=1 where the sandbox is refused (GitHub's runners).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { join, extname, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from '../../tools/vscode-iec/webview-ui/gesture-harness/cdp.mjs';

const dist = fileURLToPath(new URL('../dist/', import.meta.url));
const types = {
	'.html': 'text/html',
	'.js': 'text/javascript',
	'.mjs': 'text/javascript',
	'.json': 'application/json',
	'.wasm': 'application/wasm',
	'.css': 'text/css',
	'.svg': 'image/svg+xml',
	'.png': 'image/png',
	'.woff': 'font/woff',
	'.woff2': 'font/woff2',
	'.ttf': 'font/ttf'
};

// A project with the files whose schemas are checked. The status bar names
// a schema by its title, which it has only once the schema has loaded; one
// that matched but could not load shows its URI instead.
const project = {
	title: 'yaml-schema',
	files: {
		'nautilus.yaml': 'name: schemas\ntag-files: [tags/plant.yaml]\ntasks:\n  - program: main.st\n    scan: 100ms\n',
		'tags/plant.yaml': '- { name: Level, role: state, type: REAL, init: 0.0 }\n',
		'main.st': 'PROGRAM main\nVAR_EXTERNAL Level : REAL; END_VAR\nEND_PROGRAM\n'
	}
};

let server, base;
before(async () => {
	server = createServer(async (req, res) => {
		const path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
		if (path.startsWith('/test-project/')) {
			const open = path.slice('/test-project/'.length);
			res.writeHead(200, { 'content-type': 'application/json' });
			return res.end(JSON.stringify({ ...project, open: [open] }));
		}
		const file = normalize(join(dist, path.endsWith('/') ? path + 'index.html' : path));
		if (!file.startsWith(dist)) return res.writeHead(403).end();
		try {
			const body = await readFile(file);
			res.writeHead(200, { 'content-type': types[extname(file)] ?? 'application/octet-stream' });
			res.end(body);
		} catch {
			res.writeHead(404).end();
		}
	});
	await new Promise((r) => server.listen(0, '127.0.0.1', r));
	base = `http://127.0.0.1:${server.address().port}`;
});
after(() => server?.close());

/** The status bar's text once `want` shows up in it, or after `ms` the
 * text it had (and the page's console) for the failure message. */
async function statusBarShows(b, want, ms = 90000) {
	let text = '';
	for (const until = Date.now() + ms; Date.now() < until; ) {
		text = await b.eval(`[...document.querySelectorAll('.statusbar-item')].map((e) => e.innerText.trim()).filter(Boolean).join(' | ')`);
		if (text.includes(want)) return text;
		await new Promise((r) => setTimeout(r, 500));
	}
	assert.fail(`status bar never named the schema "${want}"; it showed: ${text}\n` + b.consoleLines.slice(-20).join('\n'));
}

for (const [file, schema] of [
	['nautilus.yaml', 'nautilus project manifest'],
	['tags/plant.yaml', 'nautilus tag file']
]) {
	test(`${file} gets its schema (${schema})`, { timeout: 180000 }, async () => {
		const b = await Browser.launch();
		try {
			await b.navigate(`${base}/app/?project=${encodeURIComponent(`${base}/test-project/${file}`)}`);
			await statusBarShows(b, schema);
		} finally {
			await b.close();
		}
	});
}
