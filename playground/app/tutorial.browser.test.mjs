// The interactive tutorial's mechanics, in a real (headless) Chrome over the
// built playground (run after scripts/build.sh):
//
//     node --test playground/app/tutorial.browser.test.mjs
//
// 1. The app in ?tutorial=1 mode (main.js lessonApi): for every lesson in
//    website/lessons, Check is red on start/ and green on solution/, with the
//    project swapped IN PLACE (the workbench is not reloaded: a marker set on
//    the window survives), and Check leaves no test file behind.
// 2. The shell page (website/src/pages/learn/interactive/[id].astro), when the
//    docs site has been built (npm run build in website/): Check, Solve, Next
//    and Start over, as a visitor presses them.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { join, extname, normalize, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from '../../tools/vscode-iec/webview-ui/gesture-harness/cdp.mjs';

const dist = fileURLToPath(new URL('../dist/', import.meta.url));
const site = fileURLToPath(new URL('../../website/dist/', import.meta.url));
const lessonsDir = fileURLToPath(new URL('../../website/lessons/', import.meta.url));
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

function tree(dir, base = dir, into = {}) {
	if (!existsSync(dir)) return into;
	for (const n of readdirSync(dir).sort()) {
		const p = join(dir, n);
		if (statSync(p).isDirectory()) tree(p, base, into);
		else into[relative(base, p)] = readFileSync(p, 'utf8');
	}
	return into;
}
const lessonIds = readdirSync(lessonsDir).filter((d) => existsSync(join(lessonsDir, d, 'lesson.md')));
const lesson = (id) => ({
	start: tree(join(lessonsDir, id, 'start')),
	solution: tree(join(lessonsDir, id, 'solution')),
	check: tree(join(lessonsDir, id, 'check')),
	open: (readFileSync(join(lessonsDir, id, 'lesson.md'), 'utf8').match(/^open:\s*\[(.*)\]/m)?.[1] ?? 'nautilus.yaml').split(',').map((s) => s.trim())
});

let server, base;
before(async () => {
	server = createServer(async (req, res) => {
		const path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
		const m = path.match(/^\/__lesson\/([\w-]+)\.start\.json$/);
		if (m && lessonIds.includes(m[1])) {
			const l = lesson(m[1]);
			return res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({ title: m[1], open: l.open, files: l.start }));
		}
		// The playground at /playground/engine/, as the docs site has it; the
		// docs site's own pages (when built) everywhere else.
		const engine = path.startsWith('/playground/engine/');
		const root = engine ? dist : site;
		const rel = engine ? path.slice('/playground/engine/'.length) : path;
		const file = normalize(join(root, rel.endsWith('/') ? rel + 'index.html' : rel));
		if (!file.startsWith(root)) return res.writeHead(403).end();
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

async function waitFor(b, what, expr, ms = 150000) {
	const end = Date.now() + ms;
	let last;
	while (Date.now() < end) {
		last = await b.eval(expr).catch((e) => String(e));
		if (last === true) return;
		await new Promise((r) => setTimeout(r, 250));
	}
	assert.fail(`no "${what}" within ${ms / 1000} s (last: ${JSON.stringify(last)})`);
}

for (const id of lessonIds) {
	test(`${id}: Check is red on start, green on solution, in one workbench`, { timeout: 300000 }, async () => {
		const l = lesson(id);
		const b = await Browser.launch();
		try {
			await b.send('Page.addScriptToEvaluateOnNewDocument', { source: `addEventListener('nautilus-playground-ready', () => (window.__playgroundReady = true));` });
			await b.navigate(`${base}/playground/engine/app/?tutorial=1&project=${encodeURIComponent(`/__lesson/${id}.start.json`)}`);
			await waitFor(b, 'workbench ready', 'window.__playgroundReady === true && !!window.nautilusPlayground.lesson');
			await b.eval('window.__same = 1');
			const run = (expr) => b.eval(`(async () => JSON.stringify((await (${expr})) ?? null))()`).then(JSON.parse);
			const check = () => run(`window.nautilusPlayground.lesson.check(${JSON.stringify(l.check)})`);
			const files = () => run('window.nautilusPlayground.lesson.files()');

			let r = await check();
			assert.ok(r.length > 0 && r.every((x) => !x.error), `start: tests ran: ${JSON.stringify(r)}`);
			assert.ok(r.some((x) => x.passed === false), `start must fail Check: ${JSON.stringify(r)}`);
			assert.ok(r.some((x) => x.passed === false && x.failure?.reason), 'a failing check says why');
			assert.deepEqual(Object.keys(await files()).sort(), Object.keys(l.start).sort(), 'Check leaves no test file behind');

			await run(`window.nautilusPlayground.lesson.load(${JSON.stringify({ files: l.solution, open: l.open })})`);
			r = await check();
			assert.ok(r.length > 0 && r.every((x) => x.passed === true), `solution must pass Check: ${JSON.stringify(r)}`);
			assert.equal(await b.eval('window.__same'), 1, 'the workbench was not reloaded');
			assert.deepEqual(await files(), l.solution, 'the solution is what the project holds');

			await run(`window.nautilusPlayground.lesson.load(${JSON.stringify({ files: l.start, open: l.open })})`);
			r = await check();
			assert.ok(r.some((x) => x.passed === false), 'back on the start project Check is red again');
		} finally {
			await b.close();
		}
	});
}

const shell = existsSync(join(site, 'learn', 'lessons')) && existsSync(join(site, 'learn', 'interactive'));
for (const id of shell ? lessonIds : []) {
	test(`${id}: the shell's Check, Solve and Start over`, { timeout: 300000 }, async () => {
		const b = await Browser.launch();
		try {
			await b.navigate(`${base}/learn/interactive/${id}/`);
			await waitFor(b, 'controls enabled', `!document.getElementById('check').disabled`);
			const press = (name) => b.eval(`document.getElementById('${name}').click()`);
			const said = () => b.eval(`document.getElementById('result').innerText`);
			const saidAbout = async (re) => {
				await waitFor(b, `result ${re}`, `${re}.test(document.getElementById('result').innerText)`, 60000);
				return said();
			};
			await press('check');
			assert.match(await saidAbout(/still fail/), /At \d/, 'Check names what is wrong');
			await press('solve');
			await saidAbout(/solution is loaded/);
			await press('check');
			await saidAbout(/All \d+ checks pass/);
			await press('reset');
			await saidAbout(/Back to the starting project/);
			await press('check');
			await saidAbout(/still fail/);
		} finally {
			await b.close();
		}
	});
}
