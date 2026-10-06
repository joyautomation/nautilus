// Cross-reference and tag descriptions on diagram elements (INVENTORY X44,
// X45; #218, #216) — the webview half, in all three editors, by real CDP
// input against the production bundle:
//
//   X44  Shift+F12 on a selected (or last-clicked) element, or its right-
//        click "Find All References", posts {type:'xref', name, line,
//        endLine} — the name as drawn and the source lines it came from.
//        The host (src/diagramXref.ts) takes it from there; its mapping is
//        unit-tested in src/xrefTarget.test.ts and the References view is
//        a rig smoke row (tools/rig/smoke/14-lsp.sh).
//   X45  a {type:'descriptions'} message puts each name's description in
//        the element's tooltip (ladder, FBD, SFC) and, with show:true, as a
//        second line under ladder contacts and coils.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	recordClips, withPage, deliver, posted, reset, center, key, clickAt, byId, node, sleep, esc,
	LD, FBD, FBD_PID, SFC
} from './diagram-helpers.mjs';

recordClips('xref-desc');

const shiftF12 = (b) => key(b, 'F12', 'F12', 123, 8);
const xrefs = async (b) => (await posted(b)).filter((m) => m.type === 'xref');
const rightClick = async (b, pt) => {
	assert.ok(pt, 'target not rendered');
	await b.moveTo(pt.x, pt.y);
	await b.send('Input.dispatchMouseEvent', { type: 'mousePressed', x: pt.x, y: pt.y, button: 'right', buttons: 2, clickCount: 1 });
	await b.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: pt.x, y: pt.y, button: 'right', buttons: 0, clickCount: 1 });
	await sleep(150);
};
const menuItem = (b) => center(b, '.nx-xref-menu button[data-action="xref"]');
const titleOf = (b, sel) =>
	b.eval(`(() => { const el = document.querySelector(${JSON.stringify(sel)}); return el ? (el.querySelector(':scope > title')?.textContent ?? el.getAttribute('title')) : null; })()`);
const descText = (b, sel) => b.eval(`document.querySelector(${JSON.stringify(sel)})?.querySelector('text.nx-desc')?.textContent ?? null`);

// As the host sends them: lower-cased keys (IEC names are case-insensitive).
const DESCS = { a: 'Start pushbutton (momentary)', y: 'Motor 1 run', t1: 'start delay', pid1: 'level loop', pump: 'transfer pump' };
const FBD_DESCS = { a: 'Tank high level', pid1: 'level loop' };

test('X44 ladder: Shift+F12 on a selected contact, coil or block posts xref with its rung lines; nothing selected posts nothing', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await clickAt(b, await center(b, byId('contact', 'a')));
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'a', line: 5, endLine: 6 }]);
		await clickAt(b, await center(b, byId('coil', 'z')));
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'z', line: 7, endLine: 8 }]);
		await clickAt(b, await center(b, byId('fb', 't1')));
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 't1', line: 5, endLine: 6 }]);
		// Shift+F12 is the only chord: F12 alone and Ctrl+Shift+F12 post nothing.
		await reset(b);
		await key(b, 'F12', 'F12', 123);
		await key(b, 'F12', 'F12', 123, 10);
		assert.deepEqual(await xrefs(b), []);
		// A click on empty canvas clears the selection: nothing to look up.
		const r = await b.eval(`(() => { const s = document.querySelector('svg.rsvg').getBoundingClientRect(); return { x: s.right - 6, y: s.bottom - 4 }; })()`);
		await clickAt(b, r);
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), []);
		assert.deepEqual(await b.eval('[...document.querySelectorAll(".node.selected")].length'), 0);
	});
});

test('X44 ladder: right-click a coil → "Find All References" posts xref; Escape or a click away closes the menu without posting', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await reset(b);
		await rightClick(b, await center(b, byId('coil', 'y')));
		const item = await menuItem(b);
		assert.ok(item, 'the element menu opened');
		assert.match(await b.eval('document.querySelector(".nx-xref-menu").textContent'), /Find All References\s*Shift\+F12/);
		await clickAt(b, item);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'y', line: 5, endLine: 6 }]);
		assert.equal(await menuItem(b), null, 'the menu closes on its choice');
		// Escape closes it; so does a click elsewhere. Neither posts.
		await reset(b);
		await rightClick(b, await center(b, byId('contact', 'b')));
		assert.ok(await menuItem(b));
		await esc(b);
		assert.equal(await menuItem(b), null);
		await rightClick(b, await center(b, byId('contact', 'b')));
		await clickAt(b, await center(b, byId('contact', 'a')));
		assert.equal(await menuItem(b), null);
		assert.deepEqual(await xrefs(b), []);
		// A right-click on something that names no identifier opens no menu.
		await rightClick(b, await center(b, '.rungname'));
		assert.equal(await menuItem(b), null);
	});
});

test('X44 ladder read-only (L5X): a click selects nothing, yet Shift+F12 answers for the element clicked', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'Main.L5X', readOnly: true });
		await clickAt(b, await center(b, byId('contact', 'b')));
		assert.equal(await b.eval('document.querySelectorAll(".node.selected").length'), 0);
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'b', line: 7, endLine: 8 }]);
	});
});

test('X45 ladder: descriptions land in the tooltips and as a second line under contacts and coils; show:false keeps only the tooltip', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		assert.equal(await descText(b, byId('contact', 'a')), null, 'no descriptions yet: no second line');
		await deliver(b, { type: 'descriptions', descriptions: DESCS, show: true });
		assert.match(await titleOf(b, byId('contact', 'a')), /\nStart pushbutton \(momentary\)$/);
		assert.match(await titleOf(b, byId('coil', 'y')), /\nMotor 1 run$/);
		assert.match(await titleOf(b, byId('fb', 't1')), /\nstart delay$/);
		// The line fits the element: a long description is cut with an
		// ellipsis (the tooltip has it whole); a short one shows as is.
		assert.equal(await descText(b, byId('contact', 'a')), 'Start pushbu…');
		assert.equal(await descText(b, byId('coil', 'y')), 'Motor 1 run');
		// A name without a description draws no line and keeps its tooltip.
		assert.equal(await descText(b, byId('contact', 'b')), null);
		assert.doesNotMatch(await titleOf(b, byId('contact', 'b')), /\n/);
		// The second line sits under the operand, inside the rung's own svg and
		// within the element's width (it never runs over a branch rail).
		const geo = await b.eval(`(() => {
			const g = document.querySelector('[data-kind="contact"][data-id="a"]');
			const op = g.querySelector('text.operand').getBoundingClientRect();
			const d = g.querySelector('text.nx-desc').getBoundingClientRect();
			const hit = g.querySelector('rect.hit').getBoundingClientRect();
			const svg = g.closest('svg').getBoundingClientRect();
			return { below: d.top >= op.bottom - 1, inside: d.bottom <= svg.bottom + 0.5, narrow: d.left >= hit.left - 0.5 && d.right <= hit.right + 0.5 };
		})()`);
		assert.deepEqual(geo, { below: true, inside: true, narrow: true });
		// The message is not a view: the ladder is still there, and a
		// re-sent model keeps the descriptions.
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		assert.equal(await descText(b, byId('coil', 'y')), 'Motor 1 run');
		// nautilus.diagram.showDescriptions off: tooltip only.
		await deliver(b, { type: 'descriptions', descriptions: DESCS, show: false });
		assert.equal(await descText(b, byId('contact', 'a')), null);
		assert.match(await titleOf(b, byId('contact', 'a')), /\nStart pushbutton \(momentary\)$/);
		// Saved webview state still holds the MODEL (a reload restores the
		// ladder, not the descriptions message).
		assert.equal(await b.eval('window.__STATE__?.msg?.type'), 'ldModel');
	});
});

test('X44/X45 FBD: Shift+F12 on a chip or an FB instance posts its name and statement line; tooltips carry descriptions', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD_PID, title: 'p.fbd' });
		await deliver(b, { type: 'descriptions', descriptions: FBD_DESCS, show: true });
		await clickAt(b, await center(b, node('v:A')));
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'A', line: 8, endLine: 8 }]);
		await clickAt(b, await center(b, `${node('f:pid1')} .title`));
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'pid1', line: 12, endLine: 12 }]);
		// Right-click an FB pin: the member, pid1.CV.
		await reset(b);
		await rightClick(b, await center(b, `${node('f:pid1')} [data-pin="CV"]`));
		await clickAt(b, await menuItem(b));
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'pid1.CV', line: 12, endLine: 12 }]);
		// A plain block names nothing: no menu.
		await rightClick(b, await center(b, node('b:c.Y')));
		assert.equal(await menuItem(b), null);
		// Tooltips: the chip's and the instance's, description last.
		assert.match(await titleOf(b, byId('chip', 'v:A')), /\nTank high level$/);
		assert.match(await titleOf(b, byId('fb', 'f:pid1')), /\nlevel loop$/);
		assert.doesNotMatch(await titleOf(b, byId('chip', 'v:B')), /\n/);
		// FBD never draws a second line.
		assert.equal(await b.eval('document.querySelectorAll("text.nx-desc, .nx-desc").length'), 0);
	});
});

test('X44/X45 SFC: Shift+F12 on an action association or a step posts its name and the step lines; the association tooltip carries the description', async () => {
	const model = {
		...SFC,
		// Pump is a declared tag in a real project (VAR_EXTERNAL); without it the
		// SFC editor would mark the row "no ACTION or variable named Pump yet".
		vars: [...(SFC.vars ?? []), { name: 'Pump', type: 'BOOL', section: 'VAR_EXTERNAL' }],
		steps: SFC.steps.map((s) => (s.name === 'Run' ? { ...s, actions: [{ qualifier: 'N', target: 'Pump' }] } : s))
	};
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model, title: 's.sfc' });
		await deliver(b, { type: 'descriptions', descriptions: DESCS, show: true });
		const row = '[data-kind="assoc"][data-id="st:Run:0"]';
		await clickAt(b, await center(b, `${row} .assoctarget`));
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'Pump', line: 5, endLine: 6 }]);
		assert.match(await titleOf(b, row), /^N Pump\ntransfer pump$/);
		await clickAt(b, await center(b, `${byId('step', 'st:Idle')} .stepname`));
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'Idle', line: 3, endLine: 4 }]);
		// And from the right-click menu.
		await reset(b);
		await rightClick(b, await center(b, `${row} .assoctarget`));
		await clickAt(b, await menuItem(b));
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'Pump', line: 5, endLine: 6 }]);
		// Connected to a controller, a step keeps VS Code's own menu (its
		// data-vscode-context: Set Active Step): no menu of ours there, and
		// Shift+F12 still answers.
		await deliver(b, { type: 'liveValues', enabled: true, fresh: true, values: {} });
		await rightClick(b, await center(b, `${byId('step', 'st:Idle')} .stepname`));
		assert.equal(await menuItem(b), null, 'the native step menu is left alone while live');
		await reset(b);
		await shiftF12(b);
		assert.deepEqual(await xrefs(b), [{ type: 'xref', name: 'Idle', line: 3, endLine: 4 }]);
	});
});
