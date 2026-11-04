// Editor gestures from the ex01 gesture log, fixed together: an FBD insert
// lands where it was asked and leaves an arranged network alone (#78,
// finding 18); the FBD editor opens at a readable zoom (#79, finding 19);
// a ladder OR is built in one step and a branch is its own DOM element
// (#91, finding 50). Real CDP input against the production bundle,
// asserting on the DOM and on the exact ops posted.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	recordClips, sleep, withPage, deliver, reset, center, key, clickAt, fbdOps, ldOps, rect, node, byId, FBD, LD,
	opsPosted, settled
} from './diagram-helpers.mjs';

recordClips('editor-gestures');

const btnByText = (b, sel, label) =>
	b.eval(`(() => { const el = [...document.querySelectorAll(${JSON.stringify(sel)})].find((x) => x.textContent.trim() === ${JSON.stringify(label)}); if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
const viewport = (b) =>
	b.eval(`(() => { const m = new DOMMatrix(getComputedStyle(document.querySelector('.svelte-flow__viewport')).transform); const p = document.querySelector('.svelte-flow').getBoundingClientRect(); return { x: m.e, y: m.f, k: m.a, left: p.left, top: p.top, w: p.width, h: p.height }; })()`);
/** Every rendered FBD node's canvas position (its xyflow transform). */
const positions = (b) =>
	b.eval(`Object.fromEntries([...document.querySelectorAll('.svelte-flow__node')].map((n) => { const m = new DOMMatrix(getComputedStyle(n).transform); return [n.getAttribute('data-id'), [Math.round(m.e), Math.round(m.f)]]; }))`);
/** Nodes whose box overlaps `id`'s on screen. */
const overlapping = (b, id) =>
	b.eval(`(() => {
		const box = (n) => n.getBoundingClientRect();
		const me = box(document.querySelector('.svelte-flow__node[data-id="${id}"]'));
		return [...document.querySelectorAll('.svelte-flow__node:not(.svelte-flow__node-net)')].filter((n) => n.dataset.id !== '${id}').filter((n) => {
			const r = box(n);
			return r.left < me.right && me.left < r.right && r.top < me.bottom && me.top < r.bottom;
		}).map((n) => n.dataset.id);
	})()`);
const emptyPoint = (b) =>
	b.eval(`(() => {
		const pane = document.querySelector('.svelte-flow__pane').getBoundingClientRect();
		const free = (x, y) => document.elementFromPoint(x, y)?.classList.contains('svelte-flow__pane');
		for (let y = pane.bottom - 160; y > pane.top + 20; y -= 15)
			for (let x = pane.right - 220; x > pane.left + 20; x -= 15)
				if (free(x, y) && free(x + 120, y + 60) && free(x - 10, y - 10)) return { x, y };
		return null;
	})()`);

// ── #78: + add → block lands where asked; an arranged network stays put ──
// P-101's shape: Level → LE and GE (with their limits), GE drives Run.
const P101 = {
	name: 'Main',
	nodes: [
		{ id: 'v:Level', kind: 'input', label: 'Level', layer: 0, line: 5 },
		{ id: 'k:1', kind: 'input', label: '2.0', layer: 0, line: 5, src: { line: 5 } },
		{ id: 'b:w.LE', kind: 'block', label: 'LE', wire: 'LE', inputs: ['IN1', 'IN2'], outputs: ['OUT'], layer: 1, line: 5 },
		{ id: 'v:Level#2', kind: 'input', label: 'Level', layer: 0, line: 6 },
		{ id: 'k:2', kind: 'input', label: '8.0', layer: 0, line: 6, src: { line: 6 } },
		{ id: 'b:w.GE', kind: 'block', label: 'GE', wire: 'GE', inputs: ['IN1', 'IN2'], outputs: ['OUT'], layer: 1, line: 6 },
		{ id: 'c:Run', kind: 'coil', label: 'Run', layer: 2, line: 7 }
	],
	edges: [
		{ from: 'v:Level', to: 'b:w.LE', toPin: 'IN1' },
		{ from: 'k:1', to: 'b:w.LE', toPin: 'IN2' },
		{ from: 'v:Level#2', to: 'b:w.GE', toPin: 'IN1' },
		{ from: 'k:2', to: 'b:w.GE', toPin: 'IN2' },
		{ from: 'b:w.GE', fromPin: 'OUT', to: 'c:Run' }
	],
	vars: [{ name: 'Level', type: 'REAL', section: 'VAR_EXTERNAL', line: 2 }]
};
// What `naut fbd graph` returns after `both = AND(LE, GE)`: the block
// joins the network, reading both compares.
const withBoth = (m) => ({
	...m,
	nodes: [...m.nodes, { id: 'b:w.both', kind: 'block', label: 'AND', wire: 'both', inputs: ['IN1', 'IN2'], outputs: ['OUT'], layer: 2, line: 8 }],
	edges: [
		...m.edges,
		{ from: 'b:w.LE', fromPin: 'OUT', to: 'b:w.both', toPin: 'IN1' },
		{ from: 'b:w.GE', fromPin: 'OUT', to: 'b:w.both', toPin: 'IN2' }
	]
});
// The user dragged Run once: the network is arranged.
const pin = (m, id, x, y) => ({ ...m, nodes: m.nodes.map((n) => (n.id === id ? { ...n, x, y } : n)) });

async function insertBoth(b) {
	await clickAt(b, await center(b, 'button.item[data-id="block → wire"]'));
	const field = (k) =>
		`[...document.querySelectorAll('label.field')].find((l) => l.querySelector('span')?.textContent.trim() === '${k}')?.querySelector('input')`;
	// Fill the form by value (typing into it is covered elsewhere).
	for (const [k, v] of [['name', 'both'], ['function', 'AND'], ['inputs', 'LE, GE']]) {
		await b.eval(`(() => { const i = ${field(k)}; i.value = ${JSON.stringify(v)}; i.dispatchEvent(new Event('input', { bubbles: true })); })()`);
	}
	await reset(b);
	await clickAt(b, await btnByText(b, '.actions button', 'insert'));
}

test('FBD + add (#78): a double-click on empty canvas opens the palette; the block lands there and the arranged network does not move', async () => {
	await withPage(async (b) => {
		const before = pin(P101, 'c:Run', 330, 60);
		await deliver(b, { type: 'model', model: before, title: 'p101.fbd' });
		const was = await positions(b);
		const pt = await emptyPoint(b);
		assert.ok(pt, 'an empty spot of canvas');
		// The canvas point under the double-click (the camera may pan later
		// to keep the new block fully in view).
		const vp = await viewport(b);
		const want = { x: Math.round((pt.x - vp.left - vp.x) / vp.k), y: Math.round((pt.y - vp.top - vp.y) / vp.k) };
		await b.dblclick(pt.x, pt.y);
		await sleep(200);
		assert.ok(await center(b, 'button.item[data-id="block → wire"]'), 'the palette opened');
		await insertBoth(b);
		assert.deepEqual(await opsPosted(b, fbdOps, 1), [{ type: 'insertStatement', text: 'both = AND(LE, GE)' }]);
		await reset(b);
		// The host applies it and sends the new model back.
		await deliver(b, { type: 'model', model: withBoth(before), title: 'p101.fbd' });
		const ops = await opsPosted(b, fbdOps, 1);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'setLayout');
		const both = ops[0].entries.find((e) => e.node === 'b:w.both');
		assert.ok(both && Math.abs(both.x - want.x) <= 1 && Math.abs(both.y - want.y) <= 1, `both at ${JSON.stringify(both)}, asked ${JSON.stringify(want)}`);
		// Rendered right away at the asked spot, and nothing else moved.
		const now = await positions(b);
		assert.ok(Math.abs(now['b:w.both'][0] - want.x) <= 1 && Math.abs(now['b:w.both'][1] - want.y) <= 1);
		// (Within a pixel: pins are whole canvas units, auto-layout isn't.)
		const near = (p, q, what) => assert.ok(Math.abs(p[0] - q[0]) <= 1 && Math.abs(p[1] - q[1]) <= 1, `${what}: ${p} vs ${q}`);
		for (const [id, at] of Object.entries(was)) near(now[id], at, `${id} stayed put`);
		// Every other entry holds an element of the arranged network where it was.
		for (const e of ops[0].entries.filter((e) => e.node !== 'b:w.both')) near([e.x, e.y], was[e.node], e.node);
		assert.ok(ops[0].entries.length > 1, 'the fixture really re-lays the network out without the fix');
		assert.deepEqual(await overlapping(b, 'b:w.both'), [], 'lands on nothing');
	});
});

test('FBD + add (#78): with a block selected, the new block lands one column right of it; an unarranged network stays auto', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: P101, title: 'p101.fbd' });
		await clickAt(b, await center(b, `${node('b:w.LE')} .title`));
		const le = (await positions(b))['b:w.LE'];
		const leW = await b.eval(`document.querySelector('.svelte-flow__node[data-id="b:w.LE"]').offsetWidth`);
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		await insertBoth(b);
		await reset(b);
		await deliver(b, { type: 'model', model: withBoth(P101), title: 'p101.fbd' });
		const ops = await opsPosted(b, fbdOps, 1);
		assert.deepEqual(ops, [{ type: 'setLayout', entries: [{ node: 'b:w.both', x: le[0] + leW + 72, y: le[1] }] }]);
		assert.deepEqual(await overlapping(b, 'b:w.both'), [], 'lands on nothing');
	});
});

test('FBD + add (#78): nothing selected, the network arranged: the newcomer is pinned clear of the held blocks', async () => {
	await withPage(async (b) => {
		const before = pin(P101, 'c:Run', 330, 60);
		await deliver(b, { type: 'model', model: before, title: 'p101.fbd' });
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		await insertBoth(b);
		await reset(b);
		await deliver(b, { type: 'model', model: withBoth(before), title: 'p101.fbd' });
		const ops = await opsPosted(b, fbdOps, 1);
		assert.equal(ops.length, 1);
		assert.ok(ops[0].entries.some((e) => e.node === 'b:w.both'), JSON.stringify(ops));
		assert.deepEqual(await overlapping(b, 'b:w.both'), [], 'lands on nothing');
	});
});

test('FBD + add (#78): nothing selected, nothing arranged: auto-layout places it and nothing is pinned', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: P101, title: 'p101.fbd' });
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		await insertBoth(b);
		await reset(b);
		await deliver(b, { type: 'model', model: withBoth(P101), title: 'p101.fbd' });
		assert.deepEqual(await fbdOps(b), []);
	});
});

// ── #79: open at a readable zoom ─────────────────────────────────────────
const BIG = {
	name: 'Big',
	nodes: [
		{ id: 'v:a', kind: 'input', label: 'a', layer: 0, line: 3 },
		{ id: 'v:b', kind: 'input', label: 'b', layer: 0, line: 3 },
		{ id: 'b:c.y', kind: 'block', label: 'AND', inputs: ['IN1', 'IN2'], outputs: ['OUT'], layer: 1, line: 3 },
		{ id: 'c:y', kind: 'coil', label: 'y', layer: 2, line: 3 },
		...Array.from({ length: 36 }, (_, i) => [
			{ id: `v:in${i}`, kind: 'input', label: `Input_Signal_${i}`, layer: 0, line: 10 + i },
			{ id: `c:out${i}`, kind: 'coil', label: `Output_Signal_${i}`, layer: 1, line: 10 + i }
		]).flat()
	],
	edges: [
		{ from: 'v:a', to: 'b:c.y', toPin: 'IN1' },
		{ from: 'v:b', to: 'b:c.y', toPin: 'IN2' },
		{ from: 'b:c.y', fromPin: 'OUT', to: 'c:y' },
		...Array.from({ length: 36 }, (_, i) => ({ from: `v:in${i}`, to: `c:out${i}` }))
	],
	vars: []
};

test('FBD open zoom (#79): a big program opens at the readable floor on its top-left; Ctrl+0 still fits it all', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: BIG, title: 'big.fbd' });
		const vp = await viewport(b);
		assert.ok(Math.abs(vp.k - 0.9) < 0.001, `opens at 0.9, got ${vp.k}`);
		// The first network (top-left) is in view, near the corner.
		const a = await rect(b, node('v:a'));
		assert.ok(a.x - vp.left < 80 && a.y - vp.top < 80, JSON.stringify({ a, vp }));
		// Pins and titles are readable: a 7 px handle ≥ 6 px, a 12 px title ≥ 10 px.
		const h = await rect(b, `${node('b:c.y')} .svelte-flow__handle`);
		assert.ok(h.w >= 6, `handle ${h.w}px`);
		const fs = await b.eval(`(() => { const el = document.querySelector('.svelte-flow__node[data-id="b:c.y"] .title'); return parseFloat(getComputedStyle(el).fontSize) * ${vp.k}; })()`);
		assert.ok(fs >= 10, `title ${fs}px`);
		// The fit command still fits everything (far below the floor here).
		await key(b, '0', 'Digit0', 48, 2);
		await settled(b);
		assert.ok((await viewport(b)).k < 0.5, 'Ctrl+0 fits the whole program');
	});
});

test('FBD open zoom (#79): a small diagram still opens fitted; an edit never re-zooms, it pans a new element into view', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		const k0 = (await viewport(b)).k;
		assert.ok(k0 >= 0.9, `fitted at ${k0}`);
		await deliver(b, { type: 'model', model: BIG, title: 'big.fbd' });
		assert.ok(Math.abs((await viewport(b)).k - 0.9) < 0.001, 'another file: a fresh opening frame');
		// A structural edit to the same file: the camera keeps its zoom and
		// pans the new element (far below) into view.
		const more = {
			...BIG,
			nodes: [...BIG.nodes, { id: 'v:z', kind: 'input', label: 'z', layer: 0, line: 90 }, { id: 'c:zz', kind: 'coil', label: 'zz', layer: 1, line: 90 }],
			edges: [...BIG.edges, { from: 'v:z', to: 'c:zz' }]
		};
		await deliver(b, { type: 'model', model: more, title: 'big.fbd' });
		const vp = await viewport(b);
		assert.ok(Math.abs(vp.k - 0.9) < 0.001, `zoom kept, got ${vp.k}`);
		const z = await rect(b, node('c:zz'));
		assert.ok(z.y >= vp.top && z.y + z.h <= vp.top + vp.h, `the new coil is in view: ${JSON.stringify({ z, vp })}`);
	});
});

// ── #91: a multi-leg OR in one step; a branch is its own element ─────────
const LD3 = {
	...LD,
	rungs: [
		{
			name: 'r1',
			line: 5,
			endLine: 6,
			elements: [{ kind: 'contact', ref: 'a' }, { kind: 'contact', ref: 'b' }, { kind: 'contact', ref: 'c' }],
			coils: [{ kind: 'coil', ref: 'y' }]
		}
	]
};
const LD_OR = {
	...LD3,
	rungs: [{ ...LD3.rungs[0], elements: [{ kind: 'branch', legs: [[{ kind: 'contact', ref: 'a' }], [{ kind: 'contact', ref: 'b' }], [{ kind: 'contact', ref: 'c' }]] }] }]
};
const ctrlClick = async (b, pt) => {
	await b.click(pt.x, pt.y, { modifiers: 2 });
	await sleep(120);
};
/** A point where the branch's own hit area is topmost (its rails / the
 * space between legs), scanning its box. */
const branchPoint = (b, id) =>
	b.eval(`(() => {
		const g = document.querySelector('[data-kind="branch"][data-id="${id}"]');
		const r = g.querySelector('.branch-hit').getBoundingClientRect();
		for (let y = r.top + 2; y < r.bottom - 2; y += 3)
			for (let x = r.left + 1; x < r.right - 1; x += 3)
				if (document.elementFromPoint(x, y)?.classList.contains('branch-hit')) return { x, y };
		return null;
	})()`);

test('Ladder OR (#91): Ctrl-click three contacts, O → ONE wrapOr with a leg each; the OR button does the same', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD3, title: 'p.ld' });
		await clickAt(b, await center(b, byId('contact', 'a')));
		await ctrlClick(b, await center(b, byId('contact', 'b')));
		await ctrlClick(b, await center(b, byId('contact', 'c')));
		assert.equal(await b.eval(`document.querySelectorAll('.node.selected').length`), 3);
		await reset(b);
		await key(b, 'o', 'KeyO', 79);
		assert.deepEqual(await opsPosted(b, ldOps, 1), [{ type: 'wrapOr', rung: 'r1', paths: [[0], [1], [2]] }]);
		// The toolbar button: same op for the same selection.
		await deliver(b, { type: 'ldModel', model: LD3, title: 'p.ld' });
		await clickAt(b, await center(b, byId('contact', 'a')));
		await ctrlClick(b, await center(b, byId('contact', 'c')));
		await reset(b);
		await clickAt(b, await center(b, byId('chip', 'or')));
		assert.deepEqual(await opsPosted(b, ldOps, 1), [{ type: 'wrapOr', rung: 'r1', paths: [[0], [2]] }]);
	});
});

test('Ladder OR (#91): a branch is ONE element — g.branch > g.leg > g.node, labelled; click its rails to select, O adds a leg, Del deletes it', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD_OR, title: 'p.ld' });
		const id = 'branch:r1:0';
		const info = await b.eval(`(() => {
			const g = document.querySelector('[data-kind="branch"][data-id="${id}"]');
			return g && {
				label: g.getAttribute('aria-label'),
				legs: [...g.querySelectorAll(':scope > g.leg')].map((l) => [l.getAttribute('data-path'), [...l.querySelectorAll('.node')].map((n) => n.getAttribute('data-id'))]),
				spot: !!document.querySelector('[data-branch="${id}"]')
			};
		})()`);
		assert.deepEqual(info, {
			label: 'OR branch, 3 legs',
			legs: [['0.0', ['a']], ['0.1', ['b']], ['0.2', ['c']]],
			spot: true
		});
		const pt = await branchPoint(b, id);
		assert.ok(pt, 'the branch has a clickable area of its own');
		await clickAt(b, pt);
		assert.ok(await b.eval(`document.querySelector('[data-id="${id}"]').classList.contains('selected')`), 'branch selected');
		await reset(b);
		await key(b, 'o', 'KeyO', 79);
		assert.deepEqual(await opsPosted(b, ldOps, 1), [{ type: 'addLeg', rung: 'r1', path: [0] }]);
		await reset(b);
		await key(b, 'Delete', 'Delete', 46);
		assert.deepEqual(await opsPosted(b, ldOps, 1), [{ type: 'delete', rung: 'r1', path: [0] }]);
	});
});
