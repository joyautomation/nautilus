// Runs in a rendered page once it has settled (render.go) and reports
// what's wrong with it as a person would see it. Returns plain JSON.
(async () => {
	await document.fonts.ready;
	await new Promise((r) => setTimeout(r, SETTLE_MS));
	const out = { blank: false, pipes: [], outside: [], mimics: [], drawings: [], sideways: 0, width: DEVICE_WIDTH };

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

	// Each kit <Mimic>: its canvas, the scale it is drawn at, where its
	// bottom lands against the window's, and the room it had (its frame's
	// width, and the height from its top to the window's bottom).
	for (const el of document.querySelectorAll('.mimic[data-scale]')) {
		if (!shows(el)) continue;
		const r = el.getBoundingClientRect();
		const [cw, ch] = (el.dataset.canvas || '').split('x').map(Number);
		out.mimics.push({
			canvas: [cw || 0, ch || 0],
			scale: +el.dataset.scale,
			below: Math.max(0, Math.round(r.bottom - innerHeight)),
			room: [Math.round(el.clientWidth), Math.max(0, Math.round(innerHeight - Math.max(0, r.top)))],
			scrolls: el.classList.contains('scrolling')
		});
	}

	// A drawing of its own (an <svg> at least half the window wide, not in a
	// kit <Mimic>) that runs past the bottom of the window.
	for (const svg of document.querySelectorAll('svg')) {
		if (svg.parentElement && svg.parentElement.closest('svg, .mimic')) continue;
		if (!shows(svg)) continue;
		const r = svg.getBoundingClientRect();
		if (r.width < innerWidth / 2 || r.bottom <= innerHeight + 2) continue;
		const vb = svg.viewBox && svg.viewBox.baseVal;
		out.drawings.push({
			size: [Math.round(r.width), Math.round(r.height)],
			viewBox: vb && vb.width ? [Math.round(vb.width), Math.round(vb.height)] : null,
			below: Math.round(r.bottom - innerHeight)
		});
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
	const clipped = (el, svg) => {
		if (el.closest('defs, clipPath, mask, pattern, symbol, marker')) return true;
		for (let g = el; g && g !== svg; g = g.parentElement) {
			if (g.hasAttribute('clip-path') || g.hasAttribute('mask')) return true;
			const cs = getComputedStyle(g);
			if ((cs.clipPath && cs.clipPath !== 'none') || (cs.mask && cs.mask !== 'none')) return true;
		}
		return false;
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
			// clipped, masked or not drawn at all (a hatch drawn oversize and
			// clipped to its tank is a design, not a coordinate gone wrong)
			if (clipped(el, svg)) continue;
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
