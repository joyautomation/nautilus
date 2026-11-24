// The screen builder module (naut-hmi.wasm, cmd/naut-wasm-hmi) on its own,
// under Node, in the file system the worker gives it: a one-mimic project
// builds to a screen, and a broken app answers its error messages rather than
// throwing. Builds the module itself (go build), so no scripts/build.sh run
// is needed:
//
//     node --test playground/app/hmi-module.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const repo = fileURLToPath(new URL('../../', import.meta.url));
const tmp = mkdtempSync(join(tmpdir(), 'hmi-module-'));
const wasm = join(tmp, 'naut-hmi.wasm');
const goroot = execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim();
const execJs = [join(goroot, 'lib/wasm/wasm_exec.js'), join(goroot, 'misc/wasm/wasm_exec.js')].find(existsSync);

test.before(() => {
	execFileSync('go', ['build', '-trimpath', '-buildvcs=false', '-o', wasm, './cmd/naut-wasm-hmi'], {
		cwd: repo,
		env: { ...process.env, GOOS: 'js', GOARCH: 'wasm' },
		stdio: 'inherit'
	});
});
test.after(() => rmSync(tmp, { recursive: true, force: true }));

function run(name, project) {
	const file = join(tmp, name + '.json');
	writeFileSync(file, JSON.stringify(project));
	const out = execFileSync(
		process.execPath,
		[
			join(repo, 'playground/scripts/hmi-module-run.mjs'),
			wasm,
			execJs,
			join(repo, 'playground/src/memfs.js'),
			join(repo, 'internal/hmibuild/packs/packs.tar.gz'),
			file
		],
		{ encoding: 'utf8', timeout: 120000 }
	);
	return JSON.parse(out.trim().split('\n').pop());
}

test('a mimic builds to a screen', () => {
	const r = run('mimic', {
		root: '/project',
		opts: { app: '', title: 't' },
		files: {
			'nautilus.yaml': 'name: t\n',
			'tank.mimic.json': '{"version":1,"name":"tank","title":"Tank","width":400,"height":300,"nodes":[]}'
		}
	});
	assert.equal(r.ok, true, JSON.stringify(r));
	assert.ok(r.files.includes('index.html'), r.files.join(', '));
	assert.ok(r.pages.length > 0, 'the built screen lists no pages');
});

test('a broken app answers its messages', () => {
	const r = run('broken', {
		root: '/project',
		opts: { app: '/project/hmi', title: 't' },
		files: {
			'nautilus.yaml': 'name: t\n',
			'hmi/package.json': '{}',
			'hmi/src/routes/+page.svelte': '<script>let x = ;</script>\n<p>{x}</p>\n'
		}
	});
	assert.equal(r.ok, false, JSON.stringify(r));
	assert.ok(r.messages.length > 0 && r.messages.every((m) => typeof m === 'string'), JSON.stringify(r));
});

test("the project is handed to the builder as bytes: a non-UTF-8 file arrives unchanged", () => {
	// memfs.js is a classic script that defines self.createMemFS
	const sandbox = { TextEncoder, TextDecoder, Uint8Array, Map, Set, Date, Object };
	sandbox.self = sandbox;
	runInNewContext(readFileSync(join(repo, 'playground/src/memfs.js'), 'utf8'), sandbox);
	const mem = sandbox.createMemFS({ log: () => {} });
	const bytes = new Uint8Array([0xff, 0xfe, 0x00, 0x89, 0x50, 0x4e, 0x47]); // not valid UTF-8 (a PNG's start)
	mem.api.writeFile('/project/hmi/static/logo.png', bytes);
	const got = mem.api.snapshotBytes('/project')['hmi/static/logo.png'];
	assert.deepEqual([...got], [...bytes]);
	assert.notDeepEqual([...new TextEncoder().encode(mem.api.snapshot('/project')['hmi/static/logo.png'])], [...bytes], 'the text snapshot is lossy: that is why the builder gets bytes');
});

test("a screen's image and font are built into the screen byte for byte", () => {
	// A 1x1 PNG, and a "font" whose bytes are not valid UTF-8: the text path corrupted both.
	const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');
	const font = Buffer.from([0x77, 0x4f, 0x46, 0x46, 0xff, 0xfe, 0x00, 0x80, 0xc3, 0x28, 0xa0, 0xa1]);
	const r = run('assets', {
		root: '/project',
		opts: { app: '/project/hmi', title: 't' },
		files: {
			'nautilus.yaml': 'name: t\n',
			'hmi/package.json': '{}',
			'hmi/src/lib/logo.png': { b64: png.toString('base64') },
			'hmi/src/lib/face.woff': { b64: font.toString('base64') },
			'hmi/src/routes/+page.svelte':
				"<script>\n\timport logo from '../lib/logo.png';\n\timport face from '../lib/face.woff';\n</script>\n<img src={logo} alt=\"logo\" />\n<p data-face={face}>x</p>\n"
		}
	});
	assert.equal(r.ok, true, JSON.stringify(r));
	const sha = (b) => createHash('sha256').update(b).digest('hex');
	const built = (ext) => Object.entries(r.sha).filter(([k]) => k.endsWith(ext));
	assert.deepEqual(built('.png').map(([, h]) => h), [sha(png)], 'the PNG was changed on the way: ' + Object.keys(r.sha).join(', '));
	assert.deepEqual(built('.woff').map(([, h]) => h), [sha(font)], 'the font was changed on the way');
});
