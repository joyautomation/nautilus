// cdp.js — READ-ONLY questions to VS Code's webviews over the Chrome DevTools
// Protocol, so gestures.sh can aim the pointer at "step Fill" or "pin IN1 of
// block t1" instead of at a pixel measured once on one window size.
//
// Nothing here clicks, types or edits: every gesture is still xdotool on the
// real window, which is what the take records. This only asks the DOM where
// things are (getBoundingClientRect) and turns that into WINDOW coordinates
// for `xdotool mousemove --window`.
//
// Runs on the Node inside VS Code's Electron (ELECTRON_RUN_AS_NODE=1), which
// has fetch and WebSocket built in — no npm, nothing installed in the rig.
// VS Code must have been started with --remote-debugging-port=$CDP_PORT
// (prep.sh's `code` wrapper does that).
//
//   cdp.js targets                 — list the debuggable targets (debugging)
//   cdp.js eval '<js>'             — evaluate <js> in the ACTIVE webview's
//                                    content document (as `doc`), print JSON
//   cdp.js rect '<js → Element>'   — the element's box in window coordinates:
//                                    "x y w h vis" (x,y = top-left; vis: 1 =
//                                    clickable, 0 = covered, -1/2 = scrolled
//                                    off above/below, 3 = off a side), or exit 3
//   cdp.js vp                      — the webview viewport's centre, "x y"
//   cdp.js point '<js → {x, y}>'   — a CLIENT point of the active webview
//                                    (e.g. an empty spot of a canvas, found
//                                    with elementFromPoint) in window
//                                    coordinates, "x y"; exit 3 on null
//   cdp.js rects '<js → Element[]>'— one "x y w h" line per element
//   cdp.js page '<js>'             — evaluate in the workbench page itself
//
// The active webview is the largest visible <iframe class="webview"> in the
// workbench — with one editor group (ed_open_diagram guarantees it), that is
// the diagram. WEBVIEW_MATCH=<substring> picks by the iframe's src instead.

const PORT = process.env.CDP_PORT || '9229';
// launch_vscode grabs from inside the CSD shadow margin; xdotool's
// --window coordinates include it. FRAME_L/FRAME_T are those margins.
const FRAME_L = +(process.env.FRAME_L || 0);
const FRAME_T = +(process.env.FRAME_T || 0);

async function targets() {
	const r = await fetch(`http://127.0.0.1:${PORT}/json/list`);
	return r.json();
}

let seq = 0;
function session(wsUrl) {
	return new Promise((resolve, reject) => {
		const ws = new WebSocket(wsUrl);
		const pending = new Map();
		ws.onmessage = (m) => {
			const d = JSON.parse(m.data);
			if (d.id && pending.has(d.id)) {
				pending.get(d.id)(d);
				pending.delete(d.id);
			}
		};
		ws.onerror = (e) => reject(new Error('websocket: ' + (e.message || 'error')));
		ws.onopen = () =>
			resolve({
				send(method, params = {}) {
					const id = ++seq;
					ws.send(JSON.stringify({ id, method, params }));
					return new Promise((res) => pending.set(id, res));
				},
				close() {
					ws.close();
				}
			});
	});
}

async function evalIn(target, expression) {
	const s = await session(target.webSocketDebuggerUrl);
	try {
		const r = await s.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
		if (r.error) throw new Error(JSON.stringify(r.error));
		if (r.result.exceptionDetails) throw new Error(r.result.exceptionDetails.exception?.description || r.result.exceptionDetails.text);
		return r.result.result.value;
	} finally {
		s.close();
	}
}

async function pageTarget(ts) {
	const pages = ts.filter((t) => t.type === 'page' && /workbench/.test(t.url));
	if (!pages.length) throw new Error('no workbench page target');
	return pages[0];
}

// The active webview's outer iframe, in the workbench page: its box (CSS px),
// the page's devicePixelRatio, and the iframe's src (to find its target).
async function activeWebview(ts) {
	const page = await pageTarget(ts);
	const match = process.env.WEBVIEW_MATCH || '';
	const info = await evalIn(
		page,
		`(() => {
			const fr = [...document.querySelectorAll('iframe.webview')].map((f) => {
				const r = f.getBoundingClientRect();
				const cs = getComputedStyle(f);
				return { src: f.src, x: r.left, y: r.top, w: r.width, h: r.height,
					vis: r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' };
			}).filter((f) => f.vis && f.src.includes(${JSON.stringify(match)}));
			fr.sort((a, b) => b.w * b.h - a.w * a.h);
			return { dpr: window.devicePixelRatio, frame: fr[0] || null, n: fr.length };
		})()`
	);
	if (!info.frame) throw new Error('no visible webview');
	const id = new URL(info.frame.src).searchParams.get('id');
	const cands = ts.filter((t) => t.type === 'iframe' && t.url.includes(id));
	if (!cands.length) throw new Error('no CDP target for webview ' + id + ' (is VS Code started with --remote-debugging-port?)');
	return { ...info, target: cands[0] };
}

// Evaluate `body` with `doc` = the webview CONTENT document (the inner
// #active-frame VS Code loads the extension's HTML into) and `off` = that
// frame's offset inside the outer iframe.
function wrap(body) {
	return `(() => {
		const inner = document.getElementById('active-frame') || document.querySelector('iframe');
		const doc = inner && inner.contentDocument ? inner.contentDocument : document;
		const ir = inner && inner.contentDocument ? inner.getBoundingClientRect() : { left: 0, top: 0 };
		const off = { x: ir.left, y: ir.top };
		const win = doc.defaultView;
		return (${body});
	})()`;
}

function toWin(wv, off, r) {
	const k = wv.dpr;
	return [
		Math.round((wv.frame.x + off.x + r.x) * k) + FRAME_L,
		Math.round((wv.frame.y + off.y + r.y) * k) + FRAME_T,
		Math.round(r.w * k),
		Math.round(r.h * k)
	];
}

async function main() {
	const [cmd, arg] = process.argv.slice(2);
	const ts = await targets();
	if (cmd === 'targets') {
		for (const t of ts) console.log(t.type, t.url.slice(0, 160));
		return;
	}
	if (cmd === 'page') {
		console.log(JSON.stringify(await evalIn(await pageTarget(ts), arg)));
		return;
	}
	const wv = await activeWebview(ts);
	if (cmd === 'eval') {
		console.log(JSON.stringify(await evalIn(wv.target, wrap(arg))));
		return;
	}
	if (cmd === 'rect' || cmd === 'rects') {
		const many = cmd === 'rects';
		const res = await evalIn(
			wv.target,
			wrap(`(() => {
				let els = (${arg});
				if (!${many}) els = els ? [els] : [];
				// vis: 1 = the centre is in the viewport and hits this element
				// (not a panel drawn over it); 0 = in view but covered;
				// -1 / 2 = above / below the viewport; 3 = off to a side.
				const vis = (e) => {
					const r = e.getBoundingClientRect(), cx = r.left + r.width / 2, cy = r.top + r.height / 2;
					if (cy < 0) return -1;
					if (cy > win.innerHeight) return 2;
					if (cx < 0 || cx > win.innerWidth) return 3;
					// inside every scrolling ancestor's CLIENT box — its scrollbars
					// are not elements, so elementFromPoint looks straight through
					// them (a ladder rung's first contact, centred on the pane's
					// horizontal scrollbar, "hit" and the double-click scrolled)
					for (let a = e.parentElement; a && a !== doc.body; a = a.parentElement) {
						const cs = win.getComputedStyle(a);
						if (!/(auto|scroll)/.test(cs.overflow + cs.overflowX + cs.overflowY)) continue;
						const ar = a.getBoundingClientRect(), m = 4;
						const l = ar.left + a.clientLeft, t = ar.top + a.clientTop;
						if (cy < t + m) return -1;
						if (cy > t + a.clientHeight - m) return 2;
						if (cx < l + m || cx > l + a.clientWidth - m) return 3;
					}
					const h = doc.elementFromPoint(cx, cy);
					if (!h) return 0;
					if (h === e || e.contains(h) || h.contains(e)) return 1;
					// a sibling in the same SVG group (a step's name over its box)
					const p = e.parentElement;
					return p && p.tagName === 'g' && p.contains(h) ? 1 : 0;
				};
				return { off, boxes: [...els].filter(Boolean).map((e) => { const r = e.getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height, v: vis(e) }; }) };
			})()`)
		);
		if (!res.boxes.length) process.exit(3);
		for (const b of res.boxes) console.log([...toWin(wv, res.off, b), b.v].join(' '));
		return;
	}
	if (cmd === 'vp') {
		// the webview viewport's centre, in window coordinates
		const res = await evalIn(wv.target, wrap(`({ off, w: win.innerWidth, h: win.innerHeight })`));
		console.log(toWin(wv, res.off, { x: res.w / 2, y: res.h / 2, w: 0, h: 0 }).slice(0, 2).join(' '));
		return;
	}
	if (cmd === 'point') {
		const res = await evalIn(wv.target, wrap(`({ off, p: (${arg}) })`));
		if (!res.p) process.exit(3);
		console.log(toWin(wv, res.off, { x: res.p.x, y: res.p.y, w: 0, h: 0 }).slice(0, 2).join(' '));
		return;
	}
	throw new Error('usage: cdp.js targets|eval|rect|rects|vp|point|page <js>');
}

main().catch((e) => {
	console.error('cdp: ' + e.message);
	process.exit(2);
});
