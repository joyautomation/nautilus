// Runs in a rendered page once it has settled (render.go) and reports
// what's wrong with it as a person would see it. Returns plain JSON.
(async () => {
	await document.fonts.ready;
	await new Promise((r) => setTimeout(r, SETTLE_MS));
	const out = { blank: false, pipes: [], crossed: [], outside: [], mimics: [], drawings: [], fold: null, sideways: 0, width: DEVICE_WIDTH };

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

	// Pipes that pass under three or more others (the kit's <Mimic> lists
	// them on its root, most crossings first).
	for (const el of document.querySelectorAll('[data-crossed]')) {
		try {
			out.crossed.push(...JSON.parse(el.dataset.crossed));
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

	// Below the fold: controls and text an operator scrolls to reach, past
	// the bottom of the window or hidden inside a box that scrolls (or
	// clips). Drawings are reported on their own (mimics, drawings above), so
	// nothing inside an <svg> counts; a panel placed on a <Mimic> that
	// spills past its box and off the window does.
	{
		const hiddenBy = (el) => {
			for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
				const cs = getComputedStyle(a);
				if (!/(auto|scroll|hidden)/.test(cs.overflowY)) continue;
				const ar = a.getBoundingClientRect(), r = el.getBoundingClientRect();
				if (r.top >= ar.bottom - 2 || r.bottom > ar.bottom + 8) return true;
			}
			return false;
		};
		const nameOf = (el) => {
			if (el.matches('input, select, textarea')) {
				const l = (el.labels && el.labels[0]) || el.closest('label');
				return ((l && l.textContent) || el.getAttribute('aria-label') || el.name || el.placeholder || el.value || el.tagName.toLowerCase()).replace(/\s+/g, ' ').trim();
			}
			return (el.textContent || '').replace(/\s+/g, ' ').trim();
		};
		let deepest = 0;
		const names = [];
		let count = 0;
		for (const el of document.querySelectorAll('input, select, textarea, button, body *')) {
			if (el.closest('svg, script, style, template, [aria-hidden="true"]')) continue;
			const leafText = !el.matches('input, select, textarea, button') && [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
			if (!leafText && !el.matches('input, select, textarea, button')) continue;
			if (!shows(el)) continue;
			const r = el.getBoundingClientRect();
			const below = (r.top + r.bottom) / 2 > innerHeight; // mostly past the fold
			if (!below && !hiddenBy(el)) continue;
			count++;
			deepest = Math.max(deepest, Math.round(r.bottom - innerHeight));
			const n = nameOf(el);
			if (n && names.length < 4 && !names.includes(n)) names.push(n.length > 28 ? n.slice(0, 27) + '…' : n);
		}
		if (count > 0) out.fold = { count, below: Math.max(0, deepest), names };
	}

	// Labels a pipe runs through: on a kit <Mimic>, a pipe's line visible
	// inside the box of a line of text (a tag, a reading, a scale mark).
	// Visible: the first thing painted at that spot, looking down from the
	// top, is a pipe, not a filled shape or a background over it (a pipe
	// under an instrument's bubble is hidden by the bubble). The text's own
	// background doesn't hide it: a label on a patch over the line still
	// reads as the line running through it.
	out.through = [];
	{
		const pe = document.createElement('style');
		pe.textContent = '.mimic, .mimic * { pointer-events: auto !important; }';
		document.head.appendChild(pe);
		const clear = (c) => !c || c === 'none' || c === 'transparent' || /rgba\([^)]*,\s*0(\.0+)?\)$/.test(c);
		const covers = (el, own) => {
			if (own.has(el)) return false;
			const cs = getComputedStyle(el);
			if (+cs.opacity < 0.5) return false;
			if (el instanceof SVGElement) return el.matches('path, rect, circle, ellipse, polygon') && !clear(cs.fill) && +cs.fillOpacity >= 0.5;
			return !clear(cs.backgroundColor);
		};
		for (const root of document.querySelectorAll('.mimic')) {
			if (!shows(root)) continue;
			const layer = root.querySelector('svg.pipes');
			if (!layer) continue;
			const pipes = [];
			for (const g of layer.querySelectorAll('[data-pipe]')) {
				const wall = g.querySelector('path.wall');
				const m = wall && wall.getScreenCTM();
				if (!m) continue;
				const len = wall.getTotalLength(), pts = [];
				for (let s = 0; s <= len; s += 2) {
					const q = wall.getPointAtLength(s);
					pts.push([m.a * q.x + m.c * q.y + m.e, m.b * q.x + m.d * q.y + m.f]);
				}
				pipes.push({ id: g.dataset.pipe, pts });
			}
			if (!pipes.length) continue;
			const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
			const hit = new Map(); // text -> pipe
			for (let n = walker.nextNode(); n; n = walker.nextNode()) {
				const text = n.textContent.replace(/\s+/g, ' ').trim();
				const el = n.parentElement;
				if (!text || !el || el.closest('svg.pipes, title, desc, style, script')) continue;
				const cs = getComputedStyle(el);
				if (cs.visibility === 'hidden' || +cs.opacity === 0) continue;
				const own = new Set();
				for (let a = el; a && a !== root; a = a.parentElement) own.add(a);
				const range = document.createRange();
				range.selectNodeContents(n);
				let found = null;
				for (const b of range.getClientRects()) {
					if (b.width < 3 || b.height < 6) continue;
					// the glyphs, not the line box's leading
					const q = { l: b.left + 1, r: b.right - 1, t: b.top + b.height * 0.22, bt: b.bottom - b.height * 0.22 };
					for (const p of pipes) {
						let tried = 0;
						for (const [x, y] of p.pts) {
							if (x <= q.l || x >= q.r || y <= q.t || y >= q.bt) continue;
							if (x < 0 || y < 0 || x >= innerWidth || y >= innerHeight || ++tried > 4) continue;
							let on = false;
							for (const e of document.elementsFromPoint(x, y)) {
								if (layer.contains(e) && e !== layer) { on = true; break; }
								if (covers(e, own)) break;
							}
							if (on) { found = p.id; break; }
						}
						if (found) break;
					}
					if (found) break;
				}
				if (found && !hit.has(text)) hit.set(text, found);
			}
			for (const [text, pipe] of hit) out.through.push({ text: text.length > 28 ? text.slice(0, 27) + '…' : text, pipe });
		}
		pe.remove();
	}

	// Sideways scroll: the page is wider than the device. Measured against
	// the device's own width, not innerWidth: a phone widens its layout
	// viewport to fit a page that's too wide, then zooms it out to fit.
	out.sideways = Math.max(0, document.documentElement.scrollWidth - DEVICE_WIDTH);
	return out;
})()
