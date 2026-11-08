// keep.js's share links and fingerprints (node --test; the IndexedDB half
// needs a browser and is not covered here).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fingerprint, sameFiles, encodeProject, decodeProject, sharePayload } from './keep.js';

const example = {
	name: 'lift-station',
	title: 'Lift station',
	open: ['main.st'],
	hmi: 'lift-station',
	files: { 'nautilus.yaml': 'name: lift\n', 'main.st': 'PROGRAM main END_PROGRAM\n', 'old.st': 'x' }
};

test('fingerprint ignores key order and changes with content', async () => {
	const a = await fingerprint({ a: '1', b: '2' });
	assert.match(a, /^[0-9a-f]{16}$/);
	assert.equal(a, await fingerprint({ b: '2', a: '1' }));
	assert.notEqual(a, await fingerprint({ a: '1', b: '3' }));
	// A file's name and text cannot run into the next one's.
	assert.notEqual(await fingerprint({ ab: 'c' }), await fingerprint({ a: 'bc' }));
});

test('sameFiles', () => {
	assert.ok(sameFiles({ a: '1' }, { a: '1' }));
	assert.ok(!sameFiles({ a: '1' }, { a: '2' }));
	assert.ok(!sameFiles({ a: '1' }, { a: '1', b: '' }));
	assert.ok(!sameFiles({ a: '1', b: '' }, { a: '1' }));
});

test('a project not made from an example round-trips whole', async () => {
	const project = { title: 'mine', open: ['nautilus.yaml'], hmi: undefined, files: { 'nautilus.yaml': 'name: x\n', 'a/b.st': 'ünïcödé ✓\n' } };
	const code = await encodeProject(sharePayload(project, project.files, null));
	assert.match(code, /^[A-Za-z0-9_-]+$/, 'base64url, nothing to escape in a URL');
	const back = await decodeProject(code);
	assert.equal(back.title, 'mine');
	assert.deepEqual(back.files, project.files);
	assert.deepEqual(back.open, ['nautilus.yaml']);
	assert.equal(back.from, undefined);
});

test('a project made from an example carries its changes, removals and the example fingerprint', async () => {
	const files = { ...example.files, 'main.st': 'PROGRAM main (* changed *) END_PROGRAM\n', 'new.st': 'y' };
	delete files['old.st'];
	const base = await fingerprint(example.files);
	const payload = sharePayload(example, files, example, base);
	assert.deepEqual(Object.keys(payload.files).sort(), ['main.st', 'new.st'], 'unchanged files are left out');
	assert.deepEqual(payload.removed, ['old.st']);

	const back = await decodeProject(await encodeProject(payload));
	assert.equal(back.from, 'lift-station');
	assert.equal(back.base, base);
	assert.deepEqual(back.removed, ['old.st']);
	// What loadSource does with it: the changes laid over the example.
	const merged = { ...example.files, ...back.files };
	for (const f of back.removed) delete merged[f];
	assert.deepEqual(merged, files);
});

test('an unchanged example has no removed list', () => {
	assert.equal(sharePayload(example, example.files, example, 'x').removed, undefined);
});

async function encodeRaw(text) {
	const gz = new Uint8Array(await new Response(new Blob([text]).stream().pipeThrough(new CompressionStream('gzip'))).arrayBuffer());
	return Buffer.from(gz).toString('base64url');
}

test('damaged links are refused with a reason', async () => {
	const good = await encodeProject({ title: 't', files: { 'nautilus.yaml': '' } });
	await assert.rejects(decodeProject(good.slice(0, good.length >> 1)), /damaged or incomplete/);
	await assert.rejects(decodeProject('not base64 at all!'), /damaged or incomplete/);
	await assert.rejects(decodeProject(await encodeRaw('{not json')), /damaged or incomplete/);
});

test('links that do not hold a project are refused', async () => {
	for (const payload of [
		null,
		[1, 2],
		{ title: 't' },
		{ files: 'nautilus.yaml' },
		{ files: { 'main.st': '' } }, // no nautilus.yaml, and not from an example
		{ files: { 'nautilus.yaml': 42 } },
		{ from: 'lift-station', files: { 'a.st': { evil: true } } }
	]) {
		await assert.rejects(decodeProject(await encodeRaw(JSON.stringify(payload))), /does not hold a nautilus project/, JSON.stringify(payload));
	}
});

test('file names that leave the project are refused', async () => {
	for (const name of ['../x.st', '/etc/passwd', 'a//b.st', 'a/./b.st', 'a\\b.st', 'a\u0000.st', '']) {
		await assert.rejects(
			decodeProject(await encodeRaw(JSON.stringify({ files: { 'nautilus.yaml': '', [name]: '' } }))),
			/a file a project cannot have/,
			JSON.stringify(name)
		);
	}
	const ok = await decodeProject(await encodeRaw(JSON.stringify({ files: { 'nautilus.yaml': '', 'hmi/src/+page.svelte': '', '.vscode/x.json': '' } })));
	assert.equal(Object.keys(ok.files).length, 3);
});

test('odd fields are dropped, not trusted', async () => {
	const back = await decodeProject(
		await encodeRaw(
			JSON.stringify({
				title: { toString: 1 },
				open: ['ok.st', 7, null],
				hmi: '../../evil',
				from: 'lift-station/../../x',
				base: 'zz',
				removed: ['old.st', {}],
				files: { 'nautilus.yaml': '' }
			})
		)
	);
	assert.equal(back.title, 'shared project');
	assert.deepEqual(back.open, ['ok.st']);
	assert.equal(back.hmi, undefined);
	assert.equal(back.from, undefined, 'from must be a plain example name');
	assert.equal(back.base, undefined);
	assert.deepEqual(back.removed, ['old.st']);
});
