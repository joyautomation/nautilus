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
import { fileURLToPath } from 'node:url';

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
