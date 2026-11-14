// Runs in a rendered page once it has settled (render.go) and reports
// what's wrong with it as a person would see it. Returns plain JSON.
(async () => {
	await document.fonts.ready;
	await new Promise((r) => setTimeout(r, SETTLE_MS));
	const out = { blank: false, pipes: [], outside: [], sideways: 0, width: DEVICE_WIDTH };

	// Blank: nothing a person would see — no text, no drawing, no control.
	const shows = (el) => {
		const r = el.getBoundingClientRect();
		if (r.width < 8 || r.height < 8 || r.bottom < 0 || r.right < 0) return false;
		const cs = getComputedStyle(el);
		return cs.visibility !== 'hidden' && cs.display !== 'none' && +cs.opacity !== 0;
	};
	out.blank = ![...document.body.querySelectorAll('*')].some((el) => {
		if (el.matches('script,style,link,meta,noscript,template')) return false;
		const leafText = el.children.length === 0 && (el.textContent || '').trim() !== '';
		return (leafText || el.matches('svg,canvas,img,input,button,select,textarea,video')) && shows(el);
	});

	// Pipes the kit's <Mimic> couldn't draw (it lists them on its root).
	for (const el of document.querySelectorAll('[data-broken-pipes]')) {
		try {
			out.pipes.push(...JSON.parse(el.dataset.brokenPipes));
		} catch {}
	}

	// Drawings far outside their own box: a shape that reaches more than
	// half the drawing's size (and at least 150 px) past its edge is a
	// coordinate gone wrong (a number passed as text, a unit off by 1000),
	// not a design; an icon's leader line a little past its edge is fine.
	const describe = (el, svg) => {
		for (let g = el; g && g !== svg; g = g.parentElement) {
			const t = g.querySelector(':scope > title');
			if (t && t.textContent.trim()) return t.textContent.trim();
			const lbl = g.getAttribute && g.getAttribute('aria-label');
			if (lbl) return lbl;
		}
		const cls = (el.getAttribute('class') || '').split(/\s+/).filter((c) => c && !c.startsWith('svelte-'));
		return el.tagName.toLowerCase() + (cls.length ? '.' + cls.join('.') : '');
	};
	for (const svg of document.querySelectorAll('svg')) {
		if (!shows(svg)) continue;
		const box = svg.getBoundingClientRect();
		const m = Math.max(150, Math.max(box.width, box.height) * 0.5);
		let n = 0;
		const found = [];
		let worst = 0;
		for (const el of svg.querySelectorAll('path,rect,line,polyline,polygon,circle,ellipse,image')) {
			if (el.closest('svg') !== svg) continue;
			if (++n > 4000) break;
			const r = el.getBoundingClientRect();
			if (!r.width && !r.height) continue;
			const past = Math.max(box.left - r.left, box.top - r.top, r.right - box.right, r.bottom - box.bottom);
			if (past > m) {
				found.push(describe(el, svg));
				worst = Math.max(worst, past);
			}
		}
		if (found.length) {
			const names = [...new Set(found)];
			out.outside.push({
				count: found.length,
				names: names.slice(0, 3),
				more: Math.max(0, names.length - 3),
				past: Math.round(worst),
				size: Math.round(Math.max(box.width, box.height))
			});
		}
	}

	// Sideways scroll: the page is wider than the device. Measured against
	// the device's own width, not innerWidth: a phone widens its layout
	// viewport to fit a page that's too wide, then zooms it out to fit.
	out.sideways = Math.max(0, document.documentElement.scrollWidth - DEVICE_WIDTH);
	return out;
})()
