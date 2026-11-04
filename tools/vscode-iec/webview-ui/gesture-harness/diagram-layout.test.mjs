// Layout-geometry regression (#563, #571): render every sample diagram —
// each git-tracked .ld/.fbd/.sfc under examples/ plus the reproducers in
// layout-fixtures/ — in the production bundle and measure what is actually
// drawn. The elements measured are the text labels and these bodies:
//
//   LD   boxes (rect.box: `{ … }`, calls, FBs) and contact/coil/edge symbols
//        (their posts);
//   SFC  step boxes, jump glyphs (rect.jumpbox), action rows (rect.assocbg),
//        comment notes, orphan chips, and the flow lines and transition bars;
//   FBD  the xyflow nodes and every label inside them.
//
// and the checks are:
//
//   1. no text label's box intersects another text label's box (FBD: across
//      nodes);
//   2. no text label intersects a body of another element — a label may only
//      touch the bodies of the element it belongs to;
//   3. a label that lives INSIDE a box (a `{ … }` assignment, a call, a step
//      name, an FB's type/args, an action row's qualifier/target) is
//      contained by that box;
//   4. LD/SFC: nothing is drawn outside its canvas — no negative x/y, nor
//      past the far edge (the SVG clips it);
//   5. FBD: node boxes never intersect.
//
// The models in layout-fixtures/*.json are `naut <ld|fbd|sfc> graph` output;
// layout-fixtures/regen.sh rewrites them after an example changes (and the
// first test fails until it has been run for a new example).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { recordClips, withPage, deliver, HERE } from './diagram-helpers.mjs';

recordClips('diagram-layout');

const DIR = join(HERE, 'layout-fixtures');
const ROOT = join(HERE, '../../../..');
const TYPE = { fbd: 'model', ld: 'ldModel', sfc: 'sfcModel' };

// Runs in the page. Returns a list of human-readable violations.
const MEASURE = `(() => {
	const EPS = 1.5; // screen px of tolerance (the pane may be zoomed to fit)
	const out = [];
	const rect = (r) => ({ x: r.left, y: r.top, r: r.right, b: r.bottom, w: r.width, h: r.height });
	const box = (el) => rect(el.getBoundingClientRect());
	const union = (bs) => ({ x: Math.min(...bs.map((b) => b.x)), y: Math.min(...bs.map((b) => b.y)), r: Math.max(...bs.map((b) => b.r)), b: Math.max(...bs.map((b) => b.b)) });
	const hit = (a, c) => Math.min(a.r, c.r) - Math.max(a.x, c.x) > EPS && Math.min(a.b, c.b) - Math.max(a.y, c.y) > EPS;
	const inside = (a, c) => a.x >= c.x - EPS && a.r <= c.r + EPS && a.y >= c.y - EPS && a.b <= c.b + EPS;
	const label = (el) => (el.textContent || '').trim().replace(/\\s+/g, ' ').slice(0, 50);
	const describe = (el) => el.tagName + '.' + (el.getAttribute('class') || '') + ' "' + label(el) + '"';
	// The glyph cell is taller than the ink; compare the middle 70%.
	const tighten = (b) => ({ x: b.x, r: b.r, y: b.y + (b.b - b.y) * 0.15, b: b.b - (b.b - b.y) * 0.15 });
	// A line's box has no width (or no height): give it a 4 px stroke.
	const thick = (b) => {
		const cx = (b.x + b.r) / 2, cy = (b.y + b.b) / 2;
		return { x: Math.min(b.x, cx - 2), r: Math.max(b.r, cx + 2), y: Math.min(b.y, cy - 2), b: Math.max(b.b, cy + 2) };
	};
	const OWNER = 'g.node, g.step, g.trans, g.jump, g.assocrow, g.note, g.orphan';

	// ── LD / SFC: hand-drawn SVG ───────────────────────────────────────────
	const svgs = [...document.querySelectorAll('svg')].filter((s) => s.querySelector('text') && !s.closest('.palette, .svelte-flow'));
	const texts = [];
	const bodies = [];
	for (const svg of svgs) {
		const canvas = box(svg);
		for (const t of svg.querySelectorAll('text')) {
			if (!label(t)) continue;
			const b = box(t);
			if (b.w < 1 || b.h < 1) continue;
			// Edit affordances (hover "+" spots, priority dots, the jump's own
			// glyph) sit on their element by design.
			if (t.matches('.spot-plus, .priotext, .jumpmark')) continue;
			texts.push({ el: t, b: tighten(b), full: b, own: t.closest(OWNER), canvas });
		}
		const add = (el, b, kind) => bodies.push({ el, b, kind, own: el.closest(OWNER), canvas });
		for (const r of svg.querySelectorAll('rect.box, rect.jumpbox, rect.assocbg, rect.notebox, rect.orphanbox')) {
			const b = box(r);
			if (b.w >= 1) add(r, b, 'box');
		}
		// A contact / coil / edge contact: the symbol its posts draw.
		for (const g of svg.querySelectorAll('g.node')) {
			if (g.querySelector(':scope > rect.box')) continue;
			const posts = [...g.querySelectorAll(':scope > .post')].map(box);
			if (posts.length) add(g, union(posts), 'symbol');
		}
		// SFC flow lines and transition bars.
		for (const l of svg.querySelectorAll('line.flowline, line.bar')) add(l, thick(box(l)), 'line');
	}
	for (const t of texts) {
		const c = t.canvas;
		const f = t.full;
		if (f.x < c.x - EPS || f.y < c.y - EPS) out.push('negative x/y: ' + describe(t.el) + ' at ' + Math.round(f.x - c.x) + ',' + Math.round(f.y - c.y));
		else if (f.r > c.r + EPS || f.b > c.b + EPS) out.push('past the canvas edge: ' + describe(t.el));
	}
	for (const bx of bodies) {
		const c = bx.canvas;
		if (bx.kind !== 'line' && (bx.b.x < c.x - EPS || bx.b.y < c.y - EPS || bx.b.r > c.r + EPS || bx.b.b > c.b + EPS)) out.push(bx.kind + ' outside the canvas: ' + describe(bx.el));
	}
	for (let i = 0; i < texts.length; i++) {
		const t = texts[i];
		for (let j = i + 1; j < texts.length; j++) {
			if (hit(t.b, texts[j].b)) out.push('text overlaps text: ' + describe(t.el) + ' / ' + describe(texts[j].el));
		}
		for (const bx of bodies) {
			// A label may touch the bodies of what it labels (its own box, its
			// transition's bar and legs, its jump's glyph).
			if (bx.own && bx.own.contains(t.el)) continue;
			if (hit(t.b, bx.b)) out.push('text overlaps ' + bx.kind + ': ' + describe(t.el) + ' / ' + describe(bx.el));
		}
	}
	// Text that lives inside a box must be contained by it.
	const IN_BOX = '.fntext, .fbtype, .fbargs, .stepname, .assocq, .assoctarget';
	for (const t of texts) {
		if (!t.el.matches(IN_BOX) || !t.own) continue;
		const bx = t.own.querySelector('rect.box, rect.assocbg');
		if (bx && !inside(t.full, box(bx))) out.push('text not inside its box: ' + describe(t.el));
	}

	// ── FBD: xyflow nodes and their labels ────────────────────────────────
	const nodes = [...document.querySelectorAll('.svelte-flow__node')].filter((n) => !n.classList.contains('hidden'));
	const nb = nodes.map((n) => ({ el: n, b: box(n) })).filter((n) => n.b.w > 1 && n.b.h > 1);
	// A network's frame (.svelte-flow__node-net) is a band drawn behind its
	// blocks, so it is meant to hold them: frames must not overlap one
	// another, and each block must sit wholly inside exactly one frame (when
	// the diagram has any). A block is then measured against its own frame
	// only by its frame's header text, which must stay clear of it.
	const isFrame = (n) => n.el.classList.contains('svelte-flow__node-net');
	const frames = nb.filter(isFrame);
	for (const n of nb) {
		if (isFrame(n) || !frames.length) continue;
		const holders = frames.filter((f) => inside(n.b, f.b));
		if (holders.length !== 1) out.push('FBD node not inside exactly one network frame (' + holders.length + '): ' + describe(n.el));
		n.frame = holders[0];
	}
	for (let i = 0; i < nb.length; i++) {
		for (let j = i + 1; j < nb.length; j++) {
			if (isFrame(nb[i]) !== isFrame(nb[j])) continue;
			if (hit(nb[i].b, nb[j].b)) out.push('FBD nodes overlap: ' + describe(nb[i].el) + ' / ' + describe(nb[j].el));
		}
	}
	// Every run of text in a node, measured by its glyphs (a Range), not by
	// the block that holds it.
	const ftexts = [];
	for (const n of nb) {
		const walk = document.createTreeWalker(n.el, NodeFilter.SHOW_TEXT);
		for (let tn = walk.nextNode(); tn; tn = walk.nextNode()) {
			if (!tn.textContent.trim() || tn.parentElement.closest('.svelte-flow__handle, title')) continue;
			const range = document.createRange();
			range.selectNodeContents(tn);
			const r = rect(range.getBoundingClientRect());
			if (r.w < 1 || r.h < 1) continue;
			ftexts.push({ el: tn.parentElement, b: tighten(r), node: n });
		}
	}
	for (let i = 0; i < ftexts.length; i++) {
		const t = ftexts[i];
		for (let j = i + 1; j < ftexts.length; j++) {
			if (ftexts[j].node !== t.node && hit(t.b, ftexts[j].b)) out.push('FBD text overlaps text: ' + describe(t.el) + ' / ' + describe(ftexts[j].el));
		}
		for (const n of nb) {
			if (n === t.node || (isFrame(n) && t.node.frame === n)) continue;
			if (hit(t.b, n.b)) out.push('FBD text overlaps a node: ' + describe(t.el) + ' / ' + describe(n.el));
		}
	}
	return { out, counts: { svgs: svgs.length, texts: texts.length, bodies: bodies.length, nodes: nb.length, frames: frames.length, fbdTexts: ftexts.length } };
})()`;

// Known, still-open overlaps, by fixture: a pattern for each exact message.
// Anything else in that fixture still fails, and so does an entry that no
// longer matches anything (delete it once it is fixed). Empty: none known.
const KNOWN = {};

const fixtures = readdirSync(DIR).filter((f) => f.endsWith('.json')).sort();

// The fixture name regen.sh writes for a source path (same rule).
const fixtureName = (path) => path.replace(/^examples\//, '').replace(/^layout-fixtures\//, 'repro/').replaceAll('/', '__') + '.json';

test('layout fixtures: exactly one for every tracked example diagram and reproducer', () => {
	// git-tracked only: an untracked project under examples/ (a live site
	// checked out for testing) must never be turned into a committed fixture.
	const tracked = execFileSync('git', ['ls-files', 'examples/*.ld', 'examples/*.fbd', 'examples/*.sfc'], { cwd: ROOT, encoding: 'utf8' })
		.split('\n')
		.filter(Boolean);
	assert.ok(tracked.length > 0, 'git ls-files found no example diagrams');
	const repros = readdirSync(DIR).filter((f) => /\.(ld|fbd|sfc)$/.test(f)).map((f) => 'layout-fixtures/' + f);
	const want = [...tracked, ...repros].map(fixtureName).sort();
	assert.deepEqual(fixtures, want, 'layout-fixtures/ is out of date: run layout-fixtures/regen.sh and commit the JSON');
});

for (const f of fixtures) {
	const ext = f.replace(/\.json$/, '').split('.').pop();
	const name = f.replace(/\.json$/, '');
	test(`layout: ${name} has no overlapping or overflowing labels`, async () => {
		const model = JSON.parse(readFileSync(join(DIR, f), 'utf8'));
		await withPage(async (b) => {
			await b.send('Emulation.setDeviceMetricsOverride', { width: 1600, height: 1100, deviceScaleFactor: 1, mobile: false });
			await deliver(b, { type: TYPE[ext], model, title: f });
			const { out, counts } = await b.eval(MEASURE);
			assert.ok(counts.texts > 0 || counts.nodes > 0, 'nothing rendered: ' + JSON.stringify(counts));
			const msgs = [...new Set(out)];
			const known = KNOWN[name] ?? [];
			const stale = known.filter((re) => !msgs.some((m) => re.test(m)));
			assert.deepEqual(stale, [], name + ': these KNOWN overlaps no longer occur; delete them');
			const fresh = msgs.filter((m) => !known.some((re) => re.test(m)));
			assert.deepEqual(fresh, [], name + ' draws overlapping / overflowing elements');
		});
	});
}
