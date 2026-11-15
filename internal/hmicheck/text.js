// Runs in a rendered page after probe.js (render.go, legible.go): every
// piece of text a person would see in the window, with its box, its colour
// as drawn (with the opacity it inherits) and the size it is drawn at, so
// the Go side can measure it against the pixels behind it in a screenshot.
(() => {
	const out = [];
	const opacity = (el) => {
		let o = 1;
		for (let e = el; e && e.nodeType === 1; e = e.parentElement) o *= +getComputedStyle(e).opacity;
		return o;
	};
	const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
	let n;
	while ((n = walker.nextNode()) && out.length < 1000) {
		const t = n.textContent.replace(/\s+/g, ' ').trim();
		if (!t) continue;
		const el = n.parentElement;
		if (!el || el.closest('script, style, noscript, template, title, desc, defs, [disabled], [aria-disabled="true"], [aria-hidden="true"]')) continue;
		const cs = getComputedStyle(el);
		if (cs.visibility === 'hidden' || cs.display === 'none') continue;
		const r = document.createRange();
		r.selectNodeContents(n);
		// one box per line: a text that wraps is two lines, not the rectangle
		// round both (which would cover whatever sits beside its short line)
		const lines = [...r.getClientRects()].filter((b) => b.width >= 2 && b.height >= 2 && b.right > 0 && b.bottom > 0 && b.left < innerWidth && b.top < innerHeight);
		if (lines.length === 0) continue;
		const svg = el instanceof SVGElement;
		let px = parseFloat(cs.fontSize) || 0;
		if (svg) {
			const m = el.getScreenCTM && el.getScreenCTM();
			if (m) px *= Math.sqrt(Math.abs(m.a * m.d - m.b * m.c));
		} else if (el.offsetWidth > 0) {
			px *= el.getBoundingClientRect().width / el.offsetWidth;
		}
		for (const b of lines) out.push({
			t: t.length > 28 ? t.slice(0, 27) + '…' : t,
			x: b.left,
			y: b.top,
			w: b.width,
			h: b.height,
			fg: svg ? cs.fill : cs.color,
			bold: (parseInt(cs.fontWeight) || 400) >= 600,
			o: opacity(el),
			px: Math.round(px * 10) / 10
		});
	}
	// what's in the fields: drawn text too, for what overlaps (not for
	// contrast: a field's text sits on the field, not on what's round it)
	for (const el of document.querySelectorAll('input:not([type=checkbox]):not([type=radio]):not([type=range]), select, textarea')) {
		if (out.length >= 1200) break;
		const cs = getComputedStyle(el);
		if (cs.visibility === 'hidden' || cs.display === 'none') continue;
		const b = el.getBoundingClientRect();
		if (b.width < 2 || b.height < 2 || b.right <= 0 || b.bottom <= 0 || b.left >= innerWidth || b.top >= innerHeight) continue;
		const v = el.tagName === 'SELECT' ? (el.selectedOptions[0] ? el.selectedOptions[0].textContent : '') : el.value;
		out.push({ t: '[' + (String(v).trim().slice(0, 20) || 'field') + ']', x: b.left, y: b.top, w: b.width, h: b.height, fg: cs.color, bold: false, o: opacity(el), px: parseFloat(cs.fontSize) || 0, field: true });
	}
	return { theme: document.documentElement.dataset.theme || '', items: out };
})()
