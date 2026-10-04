// S19: SFC middle-button drag pans the chart (ZoomPane scrolls); wheel scrolls.
// Neither is an edit.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withPage, deliver, reset, rect, sleep, wheel, sfcOps, posted, TALL } from './diagram-helpers.mjs';

recordClips('sfc-pan');

const scroll = (b) => b.eval(`(() => { const s = document.querySelector('.zpane .flow'); return { top: s.scrollTop, left: s.scrollLeft, max: s.scrollHeight - s.clientHeight }; })()`);

async function middleDrag(b, x0, y0, x1, y1) {
	const m = (type, x, y, buttons) => b.send('Input.dispatchMouseEvent', { type, x, y, button: type === 'mouseMoved' ? 'none' : 'middle', buttons, clickCount: 1 });
	await b.moveTo(x0, y0);
	await m('mousePressed', x0, y0, 4);
	for (let i = 1; i <= 5; i++) await m('mouseMoved', x0 + ((x1 - x0) * i) / 5, y0 + ((y1 - y0) * i) / 5, 4);
	await m('mouseReleased', x1, y1, 0);
	await sleep(120);
}

test('S19 SFC: middle-drag pans the chart by the drag delta and posts no edit; the wheel scrolls', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: TALL, title: 'long.sfc' });
		const pane = await rect(b, '.zpane .flow');
		const z0 = await b.eval(`document.querySelector('.zpct')?.textContent`);
		const s0 = await scroll(b);
		assert.ok(s0.max > 200, 'chart overflows the pane: ' + JSON.stringify(s0));
		assert.equal(s0.top, 0);
		await reset(b);
		// drag UP by 150px on empty canvas right of the chart: content moves up, scrollTop grows
		const x = pane.x + pane.w - 40;
		const y = pane.y + pane.h / 2;
		await middleDrag(b, x, y, x, y - 150);
		const s1 = await scroll(b);
		assert.ok(Math.abs(s1.top - 150) <= 2, 'scrollTop ~ +150, got ' + JSON.stringify(s1));
		// and back down
		await middleDrag(b, x, y - 150, x, y - 50);
		const s2 = await scroll(b);
		assert.ok(Math.abs(s2.top - 50) <= 2, 'scrollTop ~ 50, got ' + JSON.stringify(s2));
		assert.deepEqual(await sfcOps(b), [], 'panning is not an edit');
		assert.deepEqual((await posted(b)).filter((m) => m.type !== 'viewState'), [], 'nothing posted at all');
		// plain wheel scrolls (no ctrl => no zoom)
		await wheel(b, x, y, 120, 0);
		await sleep(150);
		const s3 = await scroll(b);
		assert.ok(s3.top > s2.top + 20, 'wheel scrolled: ' + JSON.stringify({ s2, s3 }));
		assert.equal(await b.eval(`document.querySelector('.zpct')?.textContent`), z0, 'plain wheel does not zoom');
		assert.deepEqual(await sfcOps(b), []);
	});
});
