// Enumerated live values in the diagrams (#246; INVENTORY X11): an enum's
// value streams as its member's NAME ("Run"), which alone reads exactly like
// a STRING. The extension posts the declared types it read from /api/meta
// as a `types` map beside the values (lowercased path → { t, e }), and every
// live overlay shows an enumerated value bare, in the enum style
// (.nx-pill.enum / text.liveval.enum), with the type in its tooltip
// (`Mode · enum`) — while a STRING tag holding the same text keeps its
// quotes, and a frame with no types (an older controller) renders as before.
//
// Run: `npm run test:gestures` (from webview-ui, after `npm run build`).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withPage, deliver, clickAt, FBD, LD, SFC, sleep } from './diagram-helpers.mjs';

recordClips('enum-display');

const MODE = [
	{ name: 'Idle', value: 0 },
	{ name: 'Run', value: 10 },
	{ name: 'Fault', value: 11 }
];
// The same text in both: only the declared type tells them apart.
const TYPES = { a: { t: 'Mode', e: MODE }, b: { t: 'STRING' }, mode: { t: 'Mode', e: MODE }, label: { t: 'STRING' } };
const live = (values, types) => ({ type: 'liveValues', enabled: true, fresh: true, values, forced: {}, types });

const pills = (b) =>
	b.eval(`[...document.querySelectorAll('.nx-pill')].map((el) => ({
		text: el.textContent.trim(),
		enum: el.classList.contains('enum'),
		title: el.getAttribute('title') || '',
		italic: getComputedStyle(el).fontStyle
	}))`);

const openVars = async (b) => {
	const pt = await b.eval(`(() => { const el = [...document.querySelectorAll('.bar button')].find((x) => x.textContent.trim() === 'vars'); if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
	assert.ok(pt, 'a vars button');
	await clickAt(b, pt);
	await sleep(200);
};

test('FBD: an enumerated chip reads bare in the enum style with its type; a STRING keeps its quotes', async () => {
	await withPage(async (b) => {
		const model = {
			...FBD,
			vars: [
				{ name: 'A', type: 'Mode', section: 'VAR_EXTERNAL', line: 3 },
				{ name: 'B', type: 'STRING', section: 'VAR_EXTERNAL', line: 3 }
			]
		};
		await deliver(b, { type: 'model', model, title: 'n.fbd' });
		await deliver(b, live({ a: 'Run', b: 'Run', y: false }, TYPES));
		await sleep(200);
		const ps = await pills(b);
		const en = ps.filter((p) => p.enum);
		assert.ok(en.length >= 1, `no enum pill among ${JSON.stringify(ps)}`);
		for (const p of en) {
			assert.equal(p.text, 'Run', 'bare member name');
			assert.equal(p.italic, 'italic', 'the enum style');
			assert.match(p.title, /A = Run \(Mode · enum\)/, 'the type on hover');
		}
		const str = ps.filter((p) => !p.enum && p.text === '"Run"');
		assert.ok(str.length >= 1, `the STRING chip keeps its quotes: ${JSON.stringify(ps)}`);
		assert.ok(str.every((p) => p.italic !== 'italic'), 'and the ordinary style');

		// The VARS panel pill, same rule.
		await openVars(b);
		const vp = await b.eval(`[...document.querySelectorAll('.row[data-id] .nx-pill')].map((el) => ({ id: el.closest('.row').dataset.id, text: el.textContent.trim(), enum: el.classList.contains('enum'), title: el.title }))`);
		assert.deepEqual(
			vp.map((p) => [p.id, p.text, p.enum]).sort(),
			[
				['A', 'Run', true],
				['B', '"Run"', false]
			]
		);
		assert.match(vp.find((p) => p.id === 'A').title, /Mode · enum/);

		// A frame with no types (an older controller): exactly as before.
		await deliver(b, live({ a: 'Run', b: 'Run', y: false }, undefined));
		await sleep(200);
		assert.deepEqual((await pills(b)).filter((p) => p.enum), [], 'no types, no enum styling');
		assert.ok((await pills(b)).every((p) => p.text !== 'Run'), 'and every string value quoted');

		// A value forced by its integer still reads as its member.
		await deliver(b, live({ a: 11, b: 'Run', y: false }, TYPES));
		await sleep(200);
		assert.ok((await pills(b)).some((p) => p.enum && p.text === 'Fault'), 'an enum integer shows as its member');
	});
});

test('Ladder: an enumerated operand value reads bare in the enum style; a STRING keeps its quotes', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await deliver(b, live({ a: 'Run', b: 'Run', y: true, z: true, t1: { Q: false, ET: 0 } }, TYPES));
		await sleep(200);
		const vals = await b.eval(`[...document.querySelectorAll('text.liveval')].map((t) => ({
			text: t.textContent, enum: t.classList.contains('enum'), italic: getComputedStyle(t).fontStyle,
			title: t.closest('g')?.querySelector('title')?.textContent || ''
		}))`);
		const en = vals.filter((v) => v.enum);
		assert.equal(en.length, 1, `exactly contact a is an enum: ${JSON.stringify(vals)}`);
		assert.equal(en[0].text, 'Run');
		assert.equal(en[0].italic, 'italic');
		assert.ok(vals.some((v) => !v.enum && v.text === '"Run"'), 'contact b, a STRING, keeps its quotes');
		const titles = await b.eval(`[...document.querySelectorAll('title')].map((t) => t.textContent)`);
		assert.ok(titles.some((t) => /^a = Run \(Mode · enum\)/.test(t)), `the type on hover: ${JSON.stringify(titles)}`);
	});
});

test('SFC: the vars panel shows an enumerated tag bare with its type, a STRING quoted', async () => {
	await withPage(async (b) => {
		const model = {
			...SFC,
			vars: [
				{ name: 'Mode', type: 'Mode', section: 'VAR_EXTERNAL', line: 2 },
				{ name: 'Label', type: 'STRING', section: 'VAR_EXTERNAL', line: 2 }
			]
		};
		await deliver(b, { type: 'sfcModel', model, title: 'seq.sfc' });
		await deliver(b, live({ _s_idle_x: true, _s_run_x: false, mode: 'Fault', label: 'Fault' }, TYPES));
		await sleep(200);
		await openVars(b);
		const vp = await b.eval(`[...document.querySelectorAll('.row[data-id] .nx-pill')].map((el) => ({ id: el.closest('.row').dataset.id, text: el.textContent.trim(), enum: el.classList.contains('enum'), title: el.title }))`);
		assert.deepEqual(
			vp.map((p) => [p.id, p.text, p.enum]).sort(),
			[
				['Label', '"Fault"', false],
				['Mode', 'Fault', true]
			]
		);
		assert.equal(vp.find((p) => p.id === 'Mode').title, 'Mode = Fault (Mode · enum)');
	});
});
