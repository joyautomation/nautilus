// Layout-geometry regression (#563): render every sample diagram — each
// .ld/.fbd/.sfc under examples/ plus the reproducers in layout-fixtures/ — in
// the production bundle and measure what is actually drawn:
//
//   1. no text label's box intersects another text label's box;
//   2. no text label intersects another element's box (a step, a contact, an
//      FB, …) — a label may only touch the box it belongs to;
//   3. a label that lives INSIDE a box (a `{ … }` assignment, a call, a step
//      name, an FB's type/args) is contained by that box;
//   4. nothing is drawn outside its canvas — no negative x/y, nor past the
//      far edge (the SVG clips it);
//   5. FBD: node boxes never intersect.
//
// The models in layout-fixtures/*.json are `naut <ld|fbd|sfc> graph` output;
// layout-fixtures/regen.sh rewrites them after an example changes.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { recordClips, withPage, deliver, sleep, HERE } from './diagram-helpers.mjs';

recordClips('diagram-layout');

const DIR = join(HERE, 'layout-fixtures');
const TYPE = { fbd: 'model', ld: 'ldModel', sfc: 'sfcModel' };

// Runs in the page. Returns a list of human-readable violations.
const MEASURE = `(() => {
	const EPS = 1.5; // screen px of tolerance (the pane may be zoomed to fit)
	const out = [];
	const box = (el) => { const r = el.getBoundingClientRect(); return { x: r.left, y: r.top, r: r.right, b: r.bottom, w: r.width, h: r.height }; };
	const hit = (a, c) => Math.min(a.r, c.r) - Math.max(a.x, c.x) > EPS && Math.min(a.b, c.b) - Math.max(a.y, c.y) > EPS;
	const inside = (a, c) => a.x >= c.x - EPS && a.r <= c.r + EPS && a.y >= c.y - EPS && a.b <= c.b + EPS;
	const label = (el) => (el.textContent || '').trim().replace(/\\s+/g, ' ').slice(0, 50);
	const describe = (el) => el.tagName + '.' + (el.getAttribute('class') || '') + ' "' + label(el) + '"';

	// ── LD / SFC: hand-drawn SVG ───────────────────────────────────────────
	const svgs = [...document.querySelectorAll('svg')].filter((s) => s.querySelector('text') && !s.closest('.palette, .svelte-flow'));
	const texts = [];
	const boxes = [];
	for (const svg of svgs) {
		const canvas = box(svg);
		for (const t of svg.querySelectorAll('text')) {
			if (!label(t)) continue;
			const b = box(t);
			if (b.w < 1 || b.h < 1) continue;
			// Edit affordances (hover "+" spots, priority dots, the jump's own
			// glyph) sit on their element by design.
			if (t.matches('.spot-plus, .priotext, .jumpmark')) continue;
			// The glyph cell is taller than the ink; compare the middle 70%.
			const tight = { x: b.x, r: b.r, y: b.y + b.h * 0.15, b: b.b - b.h * 0.15 };
			const own = t.closest('g.node, g.step, g.trans, g.jump');
			texts.push({ el: t, b: tight, full: b, own, canvas });
		}
		for (const r of svg.querySelectorAll('rect.box, rect.jumpbox')) {
			const b = box(r);
			if (b.w < 1) continue;
			boxes.push({ el: r, b, own: r.closest('g.node, g.step, g.trans, g.jump'), canvas });
		}
	}
	for (const t of texts) {
		const c = t.canvas;
		const f = t.full;
		if (f.x < c.x - EPS || f.y < c.y - EPS) out.push('negative x/y: ' + describe(t.el) + ' at ' + Math.round(f.x - c.x) + ',' + Math.round(f.y - c.y));
		else if (f.r > c.r + EPS || f.b > c.b + EPS) out.push('past the canvas edge: ' + describe(t.el));
	}
	for (const bx of boxes) {
		const c = bx.canvas;
		if (bx.b.x < c.x - EPS || bx.b.y < c.y - EPS || bx.b.r > c.r + EPS || bx.b.b > c.b + EPS) out.push('box outside the canvas: ' + describe(bx.el));
	}
	for (let i = 0; i < texts.length; i++) {
		for (let j = i + 1; j < texts.length; j++) {
			if (hit(texts[i].b, texts[j].b)) out.push('text overlaps text: ' + describe(texts[i].el) + ' / ' + describe(texts[j].el));
		}
		for (const bx of boxes) {
			if (bx.own && bx.own === texts[i].own && !texts[i].el.closest('.trans')) continue; // a label of its own box
			if (texts[i].el.closest('.jump') && bx.el.classList.contains('jumpbox') && bx.own === texts[i].own) continue;
			if (hit(texts[i].b, bx.b)) out.push('text overlaps box: ' + describe(texts[i].el) + ' / ' + describe(bx.el));
		}
	}
	// Text that lives inside a box must be contained by it.
	const IN_BOX = '.fntext, .fbtype, .fbargs, .stepname';
	for (const t of texts) {
		if (!t.el.matches(IN_BOX) || !t.own) continue;
		const bx = t.own.querySelector('rect.box');
		if (bx && !inside(t.full, box(bx))) out.push('text not inside its box: ' + describe(t.el));
	}

	// ── FBD: xyflow nodes ─────────────────────────────────────────────────
	const nodes = [...document.querySelectorAll('.svelte-flow__node')].filter((n) => !n.classList.contains('hidden'));
	const nb = nodes.map((n) => ({ el: n, b: box(n) })).filter((n) => n.b.w > 1 && n.b.h > 1);
	for (let i = 0; i < nb.length; i++) {
		for (let j = i + 1; j < nb.length; j++) {
			if (hit(nb[i].b, nb[j].b)) out.push('FBD nodes overlap: ' + describe(nb[i].el) + ' / ' + describe(nb[j].el));
		}
	}
	return { out, counts: { svgs: svgs.length, texts: texts.length, boxes: boxes.length, nodes: nb.length } };
})()`;

// Known, still-open overlaps in the examples (not #563's two bugs): several
// jump-backs out of ONE step that also has a forward transition hang their
// glyphs and labels in the forward bar's band (examples/batch-skid/phases.sfc:
// Agitate, Hold, ChargeA/B). Each entry is a pattern for the exact message;
// anything else in the file still fails. Delete an entry when it is fixed.
const KNOWN = {
	'batch-skid__phases.sfc': [
		/jumplabel.*"↩ Aborted: Abort" \/ text\.tname.*"t_[ab]_done"/,
		/tname.*"t_hold_hold" \/ text\.cond.*"holdTmr\.Q"/
	]
};

const fixtures = readdirSync(DIR).filter((f) => f.endsWith('.json')).sort();

test('layout fixtures exist for every example diagram', () => {
	assert.ok(fixtures.length >= 11, 'expected the examples/ models plus the reproducers, got ' + fixtures.length);
});

for (const f of fixtures) {
	const ext = f.replace(/\.json$/, '').split('.').pop();
	test(`layout #563: ${f.replace(/\.json$/, '')} has no overlapping or overflowing labels`, async () => {
		const model = JSON.parse(readFileSync(join(DIR, f), 'utf8'));
		await withPage(async (b) => {
			await b.send('Emulation.setDeviceMetricsOverride', { width: 1600, height: 1100, deviceScaleFactor: 1, mobile: false });
			await deliver(b, { type: TYPE[ext], model, title: f });
			await sleep(800);
			const { out, counts } = await b.eval(MEASURE);
			assert.ok(counts.texts > 0 || counts.nodes > 0, 'nothing rendered: ' + JSON.stringify(counts));
			const known = KNOWN[f.replace(/\.json$/, '')] ?? [];
			const fresh = [...new Set(out)].filter((m) => !known.some((re) => re.test(m)));
			assert.deepEqual(fresh, [], f + ' draws overlapping / overflowing elements');
		});
	});
}
