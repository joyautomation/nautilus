<script lang="ts">
	// The ladder diagram, drawn RSLogix-style from the pure layout pass
	// (ladderLayout.ts) and edited through structural ops, FBD-style: a
	// gesture posts an op, `nautilus ld edit` rewrites the rung's text,
	// the ladder re-renders. Text stays the source of truth.
	//
	// Interaction notes carried over from the FBD editor and tentacle-plc:
	// node listeners are DIRECT (use:action) — Svelte 5's delegated
	// click/dblclick is unreliable on SVG inside webviews — and drags are
	// pointer-based with a movement threshold (HTML5 dnd doesn't exist for
	// SVG), resolved at pointerup via elementFromPoint with a
	// nearest-hotspot snap fallback.
	import { annotate, type Ann, type LdElement, type LdModel, type RungStatus } from './ladder';
	import { layoutRung, rungMinWidth, L, type LSpot, type LNode } from './ladderLayout';
	import { live, liveValue, formatLive } from './liveState.svelte';

	type Diag = { line: number; message: string; severity: string };

	let {
		model,
		editable = false,
		diags = [],
		status = {},
		showLive = true,
		onOp,
		onTrace,
		requestInput
	}: {
		model: LdModel;
		editable?: boolean;
		diags?: Diag[];
		status?: Record<string, RungStatus>;
		showLive?: boolean;
		onOp?: (op: Record<string, unknown>) => void;
		onTrace?: (msg: string) => void;
		requestInput?: (
			init: string,
			at: { x: number; y: number; w: number },
			commit: (v: string) => void,
			opts?: { suggest?: 'tags' | 'types' | 'functions'; multiline?: boolean }
		) => void;
	} = $props();

	type Sel = { rung: string; path?: number[]; coil?: number } | null;
	let selected = $state<Sel>(null);

	// ── model → geometry ────────────────────────────────────────────────────
	// The canvas width is the VIEW width (PLC-IDE style: rails span the
	// pane, coils hug the right rail, conditions build from the left) —
	// never below what the widest rung actually needs, so narrow panes
	// fall back to horizontal scroll. The layout already right-aligns
	// coils to whatever width it's given; this just feeds it the pane.
	const LADDER_PAD_X = 28; // .ladder's 14px left + right padding
	let viewW = $state(document.documentElement.clientWidth);

	// Width comes from the SCROLL CONTAINER's clientWidth — the document
	// width overshoots by the container's own vertical scrollbar, which
	// pushed the right rail off screen. The observer also catches that
	// scrollbar appearing/disappearing, which no window resize reports.
	function measureView(el: Element) {
		const parent = el.parentElement ?? document.documentElement;
		const update = () => (viewW = parent.clientWidth);
		update();
		const ro = new ResizeObserver(update);
		ro.observe(parent);
		return {
			destroy() {
				ro.disconnect();
			}
		};
	}
	const rungs = $derived.by(() => {
		const on = showLive && live.enabled && live.fresh;
		const resolve = (label: string) => (on ? liveValue(label) : undefined);
		const annotated = model.rungs.map((r) => {
			const all = annotate([...r.elements, ...r.coils], on ? true : undefined, resolve);
			return { r, elems: all.slice(0, r.elements.length), coils: all.slice(r.elements.length) };
		});
		const width = Math.max(
			L.MIN_WIDTH,
			viewW - LADDER_PAD_X,
			...annotated.map((a) => rungMinWidth(a.elems, a.coils.length))
		);
		return annotated.map((a) => ({
			r: a.r,
			lay: layoutRung(a.elems, a.coils, width),
			width,
			problems: diags.filter((d) => d.line >= a.r.line && d.line <= (a.r.endLine ?? a.r.line))
		}));
	});
	const canvasW = $derived(rungs.length ? rungs[0].width : L.MIN_WIDTH);

	// Rungs and comment runs interleaved in document order — comments render
	// as note blocks above whatever follows them, just like FBD's.
	// A .ld file may define ladder FUNCTION_BLOCKs (subroutines) as well as
	// its PROGRAM; each one's rungs get a heading, and the rungs themselves
	// draw and edit exactly like the program's.
	type LBlock =
		| { t: 'rung'; line: number; x: (typeof rungs)[number] }
		| { t: 'note'; line: number; idx: number; text: string }
		| { t: 'pou'; line: number; name: string; pins: string };
	const blocks = $derived.by(() => {
		const out: LBlock[] = rungs.map((x) => ({ t: 'rung' as const, line: x.r.line, x }));
		(model.comments ?? []).forEach((c, idx) => out.push({ t: 'note', line: c.line, idx, text: c.text }));
		(model.blocks ?? []).forEach((b) =>
			out.push({
				t: 'pou',
				line: b.line,
				name: b.name,
				pins: (b.pins ?? []).map((p) => `${p.dir === 'out' ? '⇢ ' : ''}${p.name} : ${p.type}`).join(', ')
			})
		);
		return out.sort((a, b) => a.line - b.line);
	});

	const wcls = (on: boolean | undefined) => (on === true ? 'on' : on === false ? 'off' : '');
	const showVal = $derived(showLive && live.enabled && live.fresh);
	// Diff-overlay note appended to an element's tooltip.
	const diffNote = (el: { _diff?: string; _was?: string }) =>
		el._diff === 'changed' ? ` — changed${el._was ? ` (was ${el._was})` : ''}` : el._diff ? ` — ${el._diff}` : '';
	const valText = (ref: string | undefined) => {
		if (!showVal || !ref) return '';
		const v = liveValue(ref);
		return v === undefined ? '' : formatLive(v);
	};
	const trunc = (s: string | undefined, n = 12) => {
		const t = s ?? '';
		return t.length <= n ? t : t.slice(0, Math.ceil(n / 2) - 1) + '…' + t.slice(t.length - Math.floor(n / 2));
	};
	const isSel = (rung: string, path?: number[], coil?: number) =>
		selected !== null &&
		selected.rung === rung &&
		JSON.stringify(selected.path) === JSON.stringify(path) &&
		selected.coil === coil;

	// ── the palette ─────────────────────────────────────────────────────────
	// Each item inserts with placeholder operands (`_` retags after) — the
	// same structure-first philosophy as FBD's palette. Click appends at
	// the selection (or the last rung); drag onto a hotspot places exactly.
	type PalItem = {
		label: string;
		title: string;
		accept: 'series' | 'coil';
		op: (rung: string, series?: number[], index?: number) => Record<string, unknown>;
	};
	let fbSeq = 1;
	const PALETTE: PalItem[] = [
		{ label: '⊣ ⊢', title: 'NO contact', accept: 'series', op: (rung, series, index) => ({ type: 'insert', rung, kind: 'contact', path: series, index }) },
		{ label: '⊣/⊢', title: 'NC contact', accept: 'series', op: (rung, series, index) => ({ type: 'insert', rung, kind: 'contact', neg: true, path: series, index }) },
		{ label: 'FN( )', title: 'function contact — inserts GT(_, 0.0) as a placeholder; dblclick it to make it ANY function: LE, EQ, ABS(x) > 0 comparisons, etc.', accept: 'series', op: (rung, series, index) => ({ type: 'insert', rung, kind: 'fn', fn: 'GT', args: '_, 0.0', path: series, index }) },
		{ label: 'TON', title: 'on-delay timer in the rung', accept: 'series', op: (rung, series, index) => ({ type: 'insert', rung, kind: 'fb', inst: `t${fbSeq++}`, fbType: 'TON', args: 'PT := T#1S', path: series, index }) },
		{ label: 'CTU', title: 'up counter in the rung', accept: 'series', op: (rung, series, index) => ({ type: 'insert', rung, kind: 'fb', inst: `c${fbSeq++}`, fbType: 'CTU', args: 'PV := 10', path: series, index }) },
		{ label: '[ | ]', title: 'parallel branch (two open legs)', accept: 'series', op: (rung, series, index) => ({ type: 'insert', rung, kind: 'branch', path: series, index }) },
		{ label: '( )', title: 'output coil', accept: 'coil', op: (rung, _s, index) => ({ type: 'insert', rung, kind: 'coil', index }) },
		{ label: '(S)', title: 'set (latch) coil', accept: 'coil', op: (rung, _s, index) => ({ type: 'insert', rung, kind: 'coil', mode: 'S', index }) },
		{ label: '(R)', title: 'reset (unlatch) coil', accept: 'coil', op: (rung, _s, index) => ({ type: 'insert', rung, kind: 'coil', mode: 'R', index }) }
	];

	function paletteClick(item: PalItem) {
		// Append at the selection's rung (after it), else the last rung's end.
		const rungName = selected?.rung ?? (model.rungs.length ? model.rungs[model.rungs.length - 1].name : '');
		if (!rungName) return;
		if (item.accept === 'coil') {
			post(item.op(rungName, undefined, 999));
			return;
		}
		let series: number[] = [];
		let index = 999;
		if (selected?.path && selected.rung === rungName) {
			series = selected.path.slice(0, -1);
			index = selected.path[selected.path.length - 1] + 1;
		}
		post(item.op(rungName, series, index));
	}

	// ── pointer drags (palette items and existing nodes) ────────────────────
	const DRAG_THRESHOLD = 5;
	const SNAP_PX = 55;
	type Drag =
		| { kind: 'palette'; item: PalItem; x: number; y: number; sx: number; sy: number; started: boolean }
		| { kind: 'node'; rung: string; node: LNode; x: number; y: number; sx: number; sy: number; started: boolean };
	let drag = $state<Drag | null>(null);

	function beginPaletteDrag(ev: PointerEvent, item: PalItem) {
		if (!editable) return;
		drag = { kind: 'palette', item, x: ev.clientX, y: ev.clientY, sx: ev.clientX, sy: ev.clientY, started: false };
	}

	function acceptsOf(d: Drag): 'series' | 'coil' {
		if (d.kind === 'palette') return d.item.accept;
		return d.node.kind === 'coil' ? 'coil' : 'series';
	}

	function dropSpot(x: number, y: number, accept: string): { rung: string; spot: LSpot } | null {
		const direct = document.elementFromPoint(x, y)?.closest?.('[data-spot]');
		const parse = (el: Element) => {
			try {
				const s = JSON.parse(el.getAttribute('data-spot') ?? '');
				return s.accept === accept ? s : null;
			} catch {
				return null;
			}
		};
		if (direct) {
			const s = parse(direct);
			if (s) return s;
		}
		// Nearest-spot snap: drops shouldn't need pixel-perfect aim.
		let best: { rung: string; spot: LSpot } | null = null;
		let bestD = SNAP_PX;
		for (const el of document.querySelectorAll('[data-spot]')) {
			const s = parse(el);
			if (!s) continue;
			const r = el.getBoundingClientRect();
			const d = Math.hypot(x - (r.left + r.width / 2), y - (r.top + r.height / 2));
			if (d < bestD) {
				bestD = d;
				best = s;
			}
		}
		return best;
	}

	function onPointerMove(ev: PointerEvent) {
		if (!drag) return;
		const started = drag.started || Math.hypot(ev.clientX - drag.sx, ev.clientY - drag.sy) > DRAG_THRESHOLD;
		drag = { ...drag, x: ev.clientX, y: ev.clientY, started };
	}

	function onPointerUp(ev: PointerEvent) {
		if (!drag) return;
		const d = drag;
		drag = null;
		if (!d.started) {
			// A palette press without movement is a click-append; a node
			// press without movement already selected on pointerdown.
			if (d.kind === 'palette') paletteClick(d.item);
			return;
		}
		const target = dropSpot(ev.clientX, ev.clientY, acceptsOf(d));
		if (!target) return;
		const { rung, spot } = target;
		if (d.kind === 'palette') {
			if (spot.op === 'leg') post({ type: 'addLeg', rung, path: spot.branch });
			else if (spot.op === 'coil') post(d.item.op(rung, undefined, spot.index));
			else post(d.item.op(rung, spot.series, spot.index));
		} else {
			const src = d.node.coil !== undefined ? { rung: d.rung, coil: d.node.coil } : { rung: d.rung, path: d.node.path };
			if (spot.op === 'coil') post({ type: 'move', ...src, toRung: rung, toCoil: true, toIndex: spot.index });
			else if (spot.op === 'insert') post({ type: 'move', ...src, toRung: rung, toPath: spot.series, toIndex: spot.index });
			// dropping a node on an add-leg spot isn't a move target (yet)
		}
	}

	// ── direct node listeners (use:action — never delegated) ───────────────
	function nodeInteract(el: Element, args: { rung: string; node: LNode }) {
		let a = args;
		const onDown = (ev: Event) => {
			const pe = ev as PointerEvent;
			if (!editable) return;
			ev.stopPropagation();
			// Take keyboard focus so Del/N/M/Ctrl+C land (window keydown is
			// dead in a webview while nothing in the document has focus).
			(el.closest('.wrap') as HTMLElement | null)?.focus({ preventScroll: true });
			selected = { rung: a.rung, path: a.node.path, coil: a.node.coil };
			drag = { kind: 'node', rung: a.rung, node: a.node, x: pe.clientX, y: pe.clientY, sx: pe.clientX, sy: pe.clientY, started: false };
		};
		// The pointerdown above selects; the browser's follow-up click would
		// bubble to the ladder background and instantly CLEAR that selection,
		// so it must not escape the node.
		const onClick = (ev: Event) => {
			if (editable) ev.stopPropagation();
		};
		const onDbl = (ev: Event) => {
			if (!editable || !requestInput) return;
			ev.stopPropagation();
			drag = null;
			const rect = (el as SVGGElement).getBoundingClientRect();
			const at = { x: rect.left, y: rect.top, w: Math.max(rect.width, 90) };
			const ann = a.node.ann;
			const addr = a.node.coil !== undefined ? { rung: a.rung, coil: a.node.coil } : { rung: a.rung, path: a.node.path };
			if (ann.el.kind === 'contact' || ann.el.kind === 'coil') {
				requestInput(ann.el.ref ?? '', at, (v) => post({ type: 'setRef', ...addr, ref: v }), { suggest: 'tags' });
			} else if (ann.el.kind === 'fn') {
				// The WHOLE call is editable — swap GT(...) for LE(...), ABS(...),
				// any function; bare args (no name+parens) keep the function.
				requestInput(
					`${ann.el.fn ?? ''}(${ann.el.args ?? ''})`,
					at,
					(v) => {
						const m = /^\s*([A-Za-z_][A-Za-z0-9_]*)\s*\((.*)\)\s*$/.exec(v);
						if (m) post({ type: 'setArgs', ...addr, fn: m[1], args: m[2] });
						else post({ type: 'setArgs', ...addr, args: v });
					},
					{ suggest: 'functions' }
				);
			} else if (ann.el.kind === 'fb') {
				requestInput(ann.el.args ?? '', at, (v) => post({ type: 'setArgs', ...addr, args: v }));
			}
		};
		el.addEventListener('pointerdown', onDown);
		el.addEventListener('click', onClick);
		el.addEventListener('dblclick', onDbl);
		return {
			update(next: { rung: string; node: LNode }) {
				a = next;
			},
			destroy() {
				el.removeEventListener('pointerdown', onDown);
				el.removeEventListener('click', onClick);
				el.removeEventListener('dblclick', onDbl);
			}
		};
	}

	function rungNameInteract(el: Element, rung: string) {
		let name = rung;
		const onDbl = (ev: Event) => {
			if (!editable || !requestInput) return;
			ev.stopPropagation();
			const rect = (el as SVGTextElement).getBoundingClientRect();
			requestInput(name, { x: rect.left, y: rect.top, w: 120 }, (v) => post({ type: 'renameRung', rung: name, name: v }));
		};
		el.addEventListener('dblclick', onDbl);
		return {
			update(next: string) {
				name = next;
			},
			destroy() {
				el.removeEventListener('dblclick', onDbl);
			}
		};
	}

	function noteInteract(el: Element, args: { idx: number; text: string }) {
		let a = args;
		const onDbl = (ev: Event) => {
			if (!editable || !requestInput) return;
			ev.stopPropagation();
			const rect = (el as HTMLElement).getBoundingClientRect();
			requestInput(
				a.text,
				{ x: rect.left, y: rect.top, w: Math.max(rect.width, 180) },
				(v) => post({ type: 'setComment', comment: a.idx, text: v }),
				{ multiline: true }
			);
		};
		el.addEventListener('dblclick', onDbl);
		return {
			update(next: { idx: number; text: string }) {
				a = next;
			},
			destroy() {
				el.removeEventListener('dblclick', onDbl);
			}
		};
	}

	function rungCommentInteract(el: Element, args: { rung: string; comment: string }) {
		let a = args;
		const onDbl = (ev: Event) => {
			if (!editable || !requestInput) return;
			ev.stopPropagation();
			const rect = (el as SVGTSpanElement).getBoundingClientRect();
			requestInput(a.comment, { x: rect.left, y: rect.top, w: Math.max(rect.width, 160) }, (v) =>
				post({ type: 'setRungComment', rung: a.rung, text: v })
			);
		};
		el.addEventListener('dblclick', onDbl);
		return {
			update(next: { rung: string; comment: string }) {
				a = next;
			},
			destroy() {
				el.removeEventListener('dblclick', onDbl);
			}
		};
	}

	function spotInteract(el: Element, args: { rung: string; spot: LSpot }) {
		let a = args;
		const onClick = (ev: Event) => {
			if (!editable) return;
			ev.stopPropagation();
			const { rung, spot } = a;
			if (spot.op === 'insert') post({ type: 'insert', rung, kind: 'contact', path: spot.series, index: spot.index });
			else if (spot.op === 'coil') post({ type: 'insert', rung, kind: 'coil', index: spot.index });
			else if (spot.op === 'leg') post({ type: 'addLeg', rung, path: spot.branch });
		};
		el.addEventListener('click', onClick);
		return {
			update(next: { rung: string; spot: LSpot }) {
				a = next;
			},
			destroy() {
				el.removeEventListener('click', onClick);
			}
		};
	}

	function addNote() {
		// Above-the-next-rung note; lands after the selected rung (or at the
		// end) with placeholder text — dblclick edits, FBD-style.
		const after = selected?.rung ?? (model.rungs.length ? model.rungs[model.rungs.length - 1].name : '');
		post({ type: 'addComment', after, text: 'note' });
	}

	function addRung() {
		let i = model.rungs.length + 1;
		const taken = new Set(model.rungs.map((r) => r.name.toLowerCase()));
		while (taken.has('rung' + i)) i++;
		const after = model.rungs.length ? model.rungs[model.rungs.length - 1].name : '';
		post({ type: 'addRung', name: 'rung' + i, after });
	}

	// ── selection actions: delete, toggles, and the clipboard ──────────────
	// Every action exists as a plain function so the palette buttons drive
	// them by mouse; the keyboard is a shortcut layer on top. Keys are
	// captured EARLY (capture phase on window + direct listener on the
	// focusable wrap) because VS Code's webview plumbing preventDefaults /
	// swallows some editing keys (Delete, Ctrl+X) before bubble listeners
	// see them — so the handler dedupes with its own WeakSet and ignores
	// defaultPrevented entirely.
	let clipboard = $state<{ el: LdElement; coil: boolean } | null>(null);

	// NOTE: `selected` is $state, so anything read from it is a reactive
	// Proxy — and proxies can't cross postMessage (structured clone throws
	// DataCloneError). Snapshot the path; post() below is the backstop.
	function selAddr(): Record<string, unknown> | null {
		const sel = selected;
		if (!sel) return null;
		return sel.coil !== undefined
			? { rung: sel.rung, coil: sel.coil }
			: { rung: sel.rung, path: sel.path ? [...sel.path] : sel.path };
	}

	// Single exit for ops: plain-JSON snapshot (no proxies can survive it)
	// and a trace instead of a silent death if posting still throws.
	function post(op: Record<string, unknown>) {
		try {
			onOp?.(JSON.parse(JSON.stringify(op)));
		} catch (e) {
			onTrace?.('post FAILED: ' + String(e) + ' op=' + String(op?.type));
		}
	}

	function doCopy(): boolean {
		const sel = selected;
		const node = sel ? findSelected(sel) : undefined;
		if (!sel || !node) return false;
		clipboard = { el: JSON.parse(JSON.stringify(node.el)), coil: sel.coil !== undefined };
		return true;
	}

	function doDelete(): boolean {
		const addr = selAddr();
		onTrace?.(`doDelete addr=${JSON.stringify(addr)}`);
		if (!addr) return false;
		post({ type: 'delete', ...addr });
		selected = null;
		return true;
	}

	function doCut(): boolean {
		return doCopy() && doDelete();
	}

	function doPaste(): boolean {
		const clip = clipboard;
		if (!clip) return false;
		const rungName = selected?.rung ?? (model.rungs.length ? model.rungs[model.rungs.length - 1].name : '');
		if (!rungName) return false;
		const el = JSON.parse(JSON.stringify(clip.el));
		if (clip.coil || el.kind === 'coil') {
			const index = selected?.rung === rungName && selected?.coil !== undefined ? selected.coil + 1 : 999;
			post({ type: 'insert', rung: rungName, kind: 'coil', index, element: el });
			return true;
		}
		let series: number[] = [];
		let index = 999;
		if (selected?.path && selected.rung === rungName) {
			series = selected.path.slice(0, -1);
			index = selected.path[selected.path.length - 1] + 1;
		}
		post({ type: 'insert', rung: rungName, kind: el.kind, path: series, index, element: el });
		return true;
	}

	const handledKeys = new WeakSet<Event>();
	function handleKey(ev: KeyboardEvent) {
		if (!editable || handledKeys.has(ev)) return;
		const ae = document.activeElement;
		if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA')) return;
		const t = ev.target as HTMLElement | null;
		if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA')) return;
		const ctrl = ev.ctrlKey || ev.metaKey;
		const node = selected ? findSelected(selected) : undefined;
		let acted = false;
		if (ctrl && ev.key === 'c') {
			acted = doCopy();
		} else if (ctrl && ev.key === 'x') {
			acted = doCut();
		} else if (ctrl && ev.key === 'v') {
			acted = doPaste();
		} else if (!ctrl && (ev.key === 'Delete' || ev.key === 'Backspace')) {
			acted = doDelete();
		} else if (!ctrl && (ev.key === 'n' || ev.key === 'N') && node?.el.kind === 'contact') {
			post({ type: 'toggleNeg', ...selAddr() });
			acted = true;
		} else if (!ctrl && (ev.key === 'm' || ev.key === 'M') && node?.el.kind === 'coil') {
			const next = !node.el.mode ? 'S' : node.el.mode === 'S' ? 'R' : '';
			post({ type: 'setCoilMode', ...selAddr(), mode: next });
			acted = true;
		} else if (!ctrl && (ev.key === 'b' || ev.key === 'B') && selected && selected.coil === undefined) {
			// Wrap the selection in a parallel branch (OR path around it).
			post({ type: 'wrapBranch', ...selAddr() });
			acted = true;
		}
		if (
			(!ctrl && (ev.key === 'Delete' || ev.key === 'Backspace')) ||
			(ctrl && (ev.key === 'c' || ev.key === 'x' || ev.key === 'v'))
		) {
			onTrace?.(
				`key ${ctrl ? 'Ctrl+' : ''}${ev.key} acted=${acted} sel=${JSON.stringify(selected)} clip=${!!clipboard} defaultPrevented=${ev.defaultPrevented}`
			);
		}
		if (acted) {
			handledKeys.add(ev);
			ev.preventDefault();
			ev.stopPropagation();
		}
	}

	function keyInteract(el: Element) {
		const h = (ev: Event) => handleKey(ev as KeyboardEvent);
		el.addEventListener('keydown', h);
		// Capture phase beats any bubble-phase interception in the webview
		// host page; the WeakSet keeps the two registrations idempotent.
		window.addEventListener('keydown', h, true);
		return {
			destroy() {
				el.removeEventListener('keydown', h);
				window.removeEventListener('keydown', h, true);
			}
		};
	}

	function findSelected(sel: NonNullable<Sel>): Ann | undefined {
		for (const { r, lay } of rungs) {
			if (r.name !== sel.rung) continue;
			for (const n of lay.nodes) {
				if (sel.coil !== undefined ? n.coil === sel.coil : JSON.stringify(n.path) === JSON.stringify(sel.path)) {
					return n.ann;
				}
			}
		}
		return undefined;
	}

	const spotData = (rung: string, s: LSpot) =>
		JSON.stringify({ rung, spot: s, accept: s.op === 'coil' ? 'coil' : 'series' });
</script>

<!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events a11y_no_noninteractive_tabindex -->
<div class="wrap" class:dragging={drag?.started} tabindex={editable ? 0 : undefined} use:keyInteract use:measureView>
	{#if editable}
		<div class="palette" onclick={(e) => e.stopPropagation()}>
			{#each PALETTE as item (item.label)}
				<button
					title="{item.title} — click to append at the selection, or drag onto a rung"
					onpointerdown={(e) => beginPaletteDrag(e, item)}
				>{item.label}</button>
			{/each}
			<span class="sep"></span>
			<button title="Add a comment (after the selection; dblclick it to edit, empty text deletes)" onclick={addNote}>//</button>
			<button title="Add a rung at the bottom" onclick={addRung}>+ rung</button>
			<span class="sep"></span>
			<button title="Cut the selected element (Ctrl+X)" disabled={!selected} onclick={() => doCut()}>✂</button>
			<button title="Copy the selected element (Ctrl+C)" disabled={!selected} onclick={() => doCopy()}>⧉</button>
			<button title="Paste after the selection (Ctrl+V)" disabled={!clipboard} onclick={() => doPaste()}>⎘</button>
			<button title="Delete the selected element (Del)" disabled={!selected} onclick={() => doDelete()}>✕</button>
		</div>
	{/if}
	<div class="ladder" onclick={() => (selected = null)}>
		{#each blocks as b (b.t === 'rung' ? 'r:' + b.x.r.name : b.t === 'pou' ? 'p:' + b.name : 'n:' + b.idx)}
			{#if b.t === 'pou'}
				<div class="pouhead" title="a FUNCTION_BLOCK written in ladder — callable from any language">
					<span class="poukw">FUNCTION_BLOCK</span> <span class="pouname">{b.name}</span>
					{#if b.pins}<span class="poupins">{b.pins}</span>{/if}
				</div>
			{:else if b.t === 'note'}
				<div
					class="note"
					class:editable
					title={editable ? 'comment — double-click to edit (Ctrl+Enter saves); empty text deletes' : undefined}
					use:noteInteract={{ idx: b.idx, text: b.text }}
				>
					{#each b.text.split('\n') as line, i (i)}<div class="noteline">{line}</div>{/each}
				</div>
			{:else}
				{@const { r, lay, problems } = b.x}
			<svg
				width={canvasW}
				height={lay.height}
				viewBox="0 0 {canvasW} {lay.height}"
				class="rsvg {status[r.name] ?? ''}"
			>
				{#if status[r.name]}
					<rect x="0" y="2" width="4" height={lay.height - 4} rx="2" class="statusbar" />
					<text x={canvasW - L.RAIL_LEFT} y="11" text-anchor="end" class="statustag">{status[r.name]}</text>
				{/if}
				<line x1={L.RAIL_X} y1="0" x2={L.RAIL_X} y2={lay.height} class="rail" />
				<line x1={canvasW - L.RAIL_X} y1="0" x2={canvasW - L.RAIL_X} y2={lay.height} class="rail" />
				<text x={problems.length ? L.RAIL_LEFT + 14 : L.RAIL_LEFT} y="11">
					<tspan
						class="rungname"
						class:bad={problems.length > 0}
						class:editable
						use:rungNameInteract={r.name}
					>{r.name}</tspan>
					{#if r.comment}
						<tspan
							dx="8"
							class="rungcomment"
							class:editable
							use:rungCommentInteract={{ rung: r.name, comment: r.comment }}
						>(* {r.comment} *)</tspan>
					{:else if editable}
						<tspan
							dx="8"
							class="rungcomment ghosttext"
							use:rungCommentInteract={{ rung: r.name, comment: '' }}
						><title>dblclick to add a rung comment</title>(* … *)</tspan>
					{/if}
				</text>
				{#if problems.length}
					<g class="diag" transform="translate({L.RAIL_LEFT + 3}, 7)">
						<title>{problems.map((p) => p.message).join('\n')}</title>
						<circle r="6" class="diagdot" />
						<text y="3" text-anchor="middle" class="diagmark">!</text>
					</g>
				{/if}

				{#each lay.wires as w, i (i)}
					<line x1={w.x1} y1={w.y1} x2={w.x2} y2={w.y2} class="w {wcls(w.on)}" />
				{/each}
				{#each lay.vlines as v, i (i)}
					<line x1={v.x} y1={v.y1} x2={v.x} y2={v.y2} class="w {wcls(v.on)}" />
				{/each}

				{#each lay.nodes as n, i (i)}
					<g
						transform="translate({n.x}, {n.y})"
						class="node"
						class:on={n.ann.val === true}
						class:off={n.ann.val === false}
						class:dadd={n.ann.el._diff === 'added'}
						class:drem={n.ann.el._diff === 'removed'}
						class:dchg={n.ann.el._diff === 'changed'}
						class:selected={isSel(r.name, n.path, n.coil)}
						class:editable
						use:nodeInteract={{ rung: r.name, node: n }}
					>
						<rect class="hit" x="-2" y={-L.LABEL_TOP} width={n.w + 4} height={n.h + L.LABEL_TOP + L.LABEL_BOT - 4} rx="3" />
						{#if n.kind === 'contact'}
							<title>{n.ann.el.ref}{n.ann.el.neg ? ' (NC)' : ''} = {formatLive(liveValue(n.ann.el.ref ?? ''))}{diffNote(n.ann.el)}{editable ? ' — dblclick: retag · N: NO/NC · B: branch around · Del · drag to move' : ''}</title>
							<line x1="0" y1={n.h / 2} x2={n.w / 2 - 5} y2={n.h / 2} class="w {wcls(n.ann.in)}" />
							<line x1={n.w / 2 + 5} y1={n.h / 2} x2={n.w} y2={n.h / 2} class="w {wcls(n.ann.out)}" />
							<line x1={n.w / 2 - 5} y1="2" x2={n.w / 2 - 5} y2={n.h - 2} class="post" />
							<line x1={n.w / 2 + 5} y1="2" x2={n.w / 2 + 5} y2={n.h - 2} class="post" />
							{#if n.ann.el.neg}
								<line x1={n.w / 2 - 9} y1={n.h - 1} x2={n.w / 2 + 9} y2="1" class="post" />
							{/if}
							<text x={n.w / 2} y={n.h + 12} text-anchor="middle" class="operand">{trunc(n.ann.el.ref)}</text>
							{#if valText(n.ann.el.ref)}
								<text x={n.w / 2} y={n.h + 24} text-anchor="middle" class="liveval" class:lit={n.ann.val === true}>{valText(n.ann.el.ref)}</text>
							{/if}
						{:else if n.kind === 'coil'}
							<title>{n.ann.el.mode ? n.ann.el.mode + ' ' : ''}{n.ann.el.ref} = {formatLive(liveValue(n.ann.el.ref ?? ''))}{diffNote(n.ann.el)}{editable ? ' — dblclick: retag · M: mode · Del · drag to reorder' : ''}</title>
							<line x1="0" y1={n.h / 2} x2={n.w / 2 - 12} y2={n.h / 2} class="w {wcls(n.ann.in)}" />
							<line x1={n.w / 2 + 12} y1={n.h / 2} x2={n.w} y2={n.h / 2} class="w {wcls(n.ann.val)}" />
							<path d="M {n.w / 2 - 8} 2 Q {n.w / 2 - 16} {n.h / 2} {n.w / 2 - 8} {n.h - 2}" fill="none" class="post" />
							<path d="M {n.w / 2 + 8} 2 Q {n.w / 2 + 16} {n.h / 2} {n.w / 2 + 8} {n.h - 2}" fill="none" class="post" />
							{#if n.ann.el.mode}
								<text x={n.w / 2} y={n.h / 2 + 4} text-anchor="middle" class="mark">{n.ann.el.mode}</text>
							{/if}
							<text x={n.w / 2} y={n.h + 12} text-anchor="middle" class="operand">{trunc(n.ann.el.ref)}</text>
							{#if valText(n.ann.el.ref)}
								<text x={n.w / 2} y={n.h + 24} text-anchor="middle" class="liveval" class:lit={n.ann.val === true}>{valText(n.ann.el.ref)}</text>
							{/if}
						{:else if n.kind === 'fn'}
							<title>{n.ann.el.fn}({n.ann.el.args}){diffNote(n.ann.el)}{editable ? ' — dblclick: edit the call (any function) · Del · drag to move' : ''}</title>
							<rect x="0" y="0" width={n.w} height={n.h} rx="4" class="box" />
							<text x={n.w / 2} y={n.h / 2 + 3.5} text-anchor="middle" class="fntext">{n.ann.el.fn}({n.ann.el.args})</text>
						{:else if n.kind === 'fb'}
							<title>{n.ann.el.inst} : {n.ann.el.type}({n.ann.el.args}){diffNote(n.ann.el)}{editable ? ' — dblclick: edit args · Del · drag to move' : ''}</title>
							<rect x="0" y="0" width={n.w} height={n.h} rx="3" class="box fbbox" />
							<text x={n.w / 2} y="-4" text-anchor="middle" class="operand inst">{n.ann.el.inst}</text>
							<text x={n.w / 2} y="15" text-anchor="middle" class="fbtype">{n.ann.el.type}</text>
							{#if n.ann.el.args}
								<text x={n.w / 2} y={n.h - 5} text-anchor="middle" class="fbargs">{trunc(n.ann.el.args, 22)}</text>
							{/if}
							<text x="3" y="9" class="pin">{n.ann.el.powerIn}</text>
							<text x={n.w - 3} y="9" text-anchor="end" class="pin">{n.ann.el.powerOut}</text>
						{/if}
						{#if n.ann.el._diff}
							<rect
								class="dmark {n.ann.el._diff}"
								x="-4"
								y={-L.LABEL_TOP - 2}
								width={n.w + 8}
								height={n.h + L.LABEL_TOP + 16}
								rx="4"
							/>
							{#if n.ann.el._diff === 'changed' && n.ann.el._was}
								<text x={n.w / 2} y={n.h + 24} text-anchor="middle" class="dwas">was {trunc(n.ann.el._was, 20)}</text>
							{:else if n.ann.el._diff === 'removed'}
								<text x={n.w / 2} y={n.h + 24} text-anchor="middle" class="dwas gone">removed</text>
							{/if}
						{/if}
					</g>
				{/each}

				{#if editable}
					{#each lay.spots as s, i (i)}
						<g
							class="spot"
							data-spot={spotData(r.name, s)}
							transform="translate({s.x}, {s.y})"
							use:spotInteract={{ rung: r.name, spot: s }}
						>
							<title>{s.op === 'insert' ? 'insert here (click: contact; or drop a palette item)' : s.op === 'coil' ? 'add a coil here' : 'add a branch leg'}</title>
							<circle r="8" class="spot-hit" />
							<circle r="4.5" class="spot-dot" />
							<text y="2.8" text-anchor="middle" class="spot-plus">+</text>
						</g>
					{/each}
				{/if}
			</svg>
			{/if}
		{/each}
	</div>
	{#if drag?.started}
		<div class="ghost" style="left: {drag.x + 10}px; top: {drag.y + 8}px">
			{drag.kind === 'palette' ? drag.item.label : (drag.node.ann.el.ref ?? drag.node.ann.el.inst ?? drag.node.ann.el.fn)}
		</div>
	{/if}
</div>

<svelte:window onpointermove={onPointerMove} onpointerup={onPointerUp} />

<style>
	.wrap {
		display: flex;
		flex-direction: column;
		width: max-content;
		min-width: 100%;
		outline: none;
	}
	.palette {
		position: sticky;
		top: 0;
		left: 0;
		z-index: 10;
		display: flex;
		gap: 4px;
		align-items: center;
		padding: 5px 14px;
		background: color-mix(in srgb, var(--nx-bg) 88%, transparent);
		border-bottom: 1px solid var(--nx-border);
		font-family: var(--nx-mono);
	}
	.palette button {
		padding: 2px 8px;
		border: 1px solid var(--nx-border);
		border-radius: 3px;
		background: transparent;
		color: var(--nx-ui-ink);
		font-size: 11px;
		font-family: var(--nx-mono);
		cursor: grab;
		white-space: nowrap;
	}
	.palette button:hover {
		border-color: var(--nx-accent);
		background: var(--nx-hover);
	}
	.palette button:disabled {
		opacity: 0.35;
		cursor: default;
	}
	.palette button:disabled:hover {
		border-color: var(--nx-border);
		background: transparent;
	}
	.palette .sep {
		width: 1px;
		height: 16px;
		background: var(--nx-border);
		margin: 0 4px;
	}
	.ladder {
		display: flex;
		flex-direction: column;
		padding: 10px 14px;
		font-family: var(--nx-mono);
		width: max-content;
	}
	svg {
		display: block;
		overflow: visible;
	}
	.rail {
		stroke: var(--nx-ink);
		stroke-width: 3;
	}
	/* diff overlay — ladder gets its OWN palette: green is power here, so
	   added = cyan, changed = amber, removed = red. */
	.wrap {
		--nxld-added: #3fc6ff;
		--nxld-changed: #e2b93d;
		--nxld-removed: var(--nx-removed);
	}
	.rsvg.removed {
		opacity: 0.45;
	}
	.rsvg.added .statusbar,
	.rsvg.added .statustag {
		fill: var(--nxld-added);
	}
	.rsvg.removed .statusbar,
	.rsvg.removed .statustag {
		fill: var(--nxld-removed);
	}
	.rsvg.changed .statusbar,
	.rsvg.changed .statustag {
		fill: var(--nxld-changed);
	}
	/* element-level diff marks inside a changed rung */
	.node.dadd .post,
	.node.dadd .box {
		stroke: var(--nxld-added);
	}
	.node.dadd .operand,
	.node.dadd .fntext,
	.node.dadd .fbtype {
		fill: var(--nxld-added);
	}
	.node.dchg .post,
	.node.dchg .box {
		stroke: var(--nxld-changed);
	}
	.node.dchg .operand,
	.node.dchg .fntext,
	.node.dchg .fbtype {
		fill: var(--nxld-changed);
	}
	.node.drem {
		opacity: 0.5;
	}
	.node.drem .post,
	.node.drem .box {
		stroke: var(--nxld-removed);
	}
	.node.drem .operand,
	.node.drem .fntext,
	.node.drem .fbtype {
		fill: var(--nxld-removed);
	}
	/* the outline box + "was …" line that make a mark unmissable */
	.dmark {
		fill: transparent;
		stroke-width: 1.4;
		stroke-dasharray: 3 2;
		pointer-events: none;
	}
	.dmark.added {
		stroke: var(--nxld-added);
	}
	.dmark.changed {
		stroke: var(--nxld-changed);
	}
	.dmark.removed {
		stroke: var(--nxld-removed);
	}
	.dwas {
		font-size: 9px;
		font-style: italic;
		fill: var(--nxld-changed);
	}
	.dwas.gone {
		text-decoration: line-through;
		fill: var(--nxld-removed);
	}
	.statustag {
		font-size: 9px;
		font-weight: 700;
		font-style: italic;
	}
	.rungname {
		font-size: 10px;
		fill: var(--nx-muted);
		font-style: italic;
	}
	.rungname.bad {
		fill: var(--nx-err);
		font-weight: 700;
	}
	.rungname.editable {
		cursor: pointer;
	}
	.rungcomment {
		font-size: 10px;
		fill: var(--nx-comment);
		font-style: italic;
	}
	.rungcomment.editable {
		cursor: pointer;
	}
	.ghosttext {
		opacity: 0;
		cursor: pointer;
		transition: opacity 0.12s;
	}
	svg:hover .ghosttext {
		opacity: 0.45;
	}
	.pouhead {
		margin: 14px 0 2px;
		padding: 3px 8px;
		border-left: 3px solid var(--vscode-textLink-foreground, #4ea1ff);
		font-size: 11px;
		opacity: 0.9;
	}
	.poukw {
		font-weight: 600;
		opacity: 0.7;
	}
	.pouname {
		font-weight: 700;
	}
	.poupins {
		opacity: 0.7;
		margin-left: 8px;
	}
	.note {
		align-self: flex-start;
		margin: 4px 0 6px 20px;
		padding: 3px 8px;
		border: 1px dashed color-mix(in srgb, var(--nx-comment) 55%, transparent);
		border-radius: 4px;
		color: var(--nx-comment);
		font-style: italic;
		font-size: 11px;
		line-height: 15px;
		white-space: pre;
	}
	.note.editable {
		cursor: text;
	}
	.noteline {
		min-height: 15px;
	}
	.diagdot {
		fill: var(--nx-err);
	}
	.diagmark {
		font-size: 9px;
		font-weight: 800;
		fill: var(--nx-bg);
	}
	.w {
		stroke: var(--nx-ink);
		stroke-width: 2;
		opacity: 0.9;
	}
	.w.on {
		stroke: var(--nx-ok);
		opacity: 1;
	}
	.w.off {
		opacity: 0.3;
	}
	.post {
		stroke: var(--nx-ink);
		stroke-width: 2;
	}
	.node.on .post {
		stroke: var(--nx-ok);
	}
	.node.off .post {
		opacity: 0.5;
	}
	.node.editable {
		cursor: grab;
	}
	.hit {
		fill: transparent;
		pointer-events: all;
	}
	.node.selected .hit {
		fill: color-mix(in srgb, var(--nx-accent) 14%, transparent);
		stroke: var(--nx-accent);
		stroke-width: 1.4;
	}
	.operand {
		font-size: 10px;
		fill: var(--nx-ui-ink);
	}
	.node.on .operand {
		fill: var(--nx-ok);
	}
	.liveval {
		font-size: 9px;
		fill: var(--nx-muted);
		font-weight: 600;
	}
	.liveval.lit {
		fill: var(--nx-ok);
	}
	.mark {
		font-size: 9px;
		font-weight: 700;
		fill: var(--nx-ui-ink);
	}
	.node.on .mark {
		fill: var(--nx-ok);
	}
	.box {
		fill: var(--nx-panel-bg);
		stroke: var(--nx-ink);
		stroke-width: 1.2;
	}
	.node.on .box {
		stroke: var(--nx-ok);
	}
	.node.off .box {
		opacity: 0.6;
	}
	.fntext {
		font-size: 10px;
		fill: var(--nx-ui-ink);
	}
	.node.on .fntext {
		fill: var(--nx-ok);
	}
	.inst {
		font-weight: 700;
	}
	.fbtype {
		font-size: 10px;
		fill: var(--nx-ui-ink);
		font-weight: 600;
	}
	.fbargs {
		font-size: 8.5px;
		fill: var(--nx-muted);
	}
	.pin {
		font-size: 7.5px;
		fill: var(--nx-muted);
	}
	.spot {
		cursor: copy;
		opacity: 0;
		transition: opacity 0.12s;
	}
	svg:hover .spot,
	.wrap.dragging .spot {
		opacity: 0.6;
	}
	.spot:hover {
		opacity: 1;
	}
	.spot-hit {
		fill: transparent;
		pointer-events: all;
	}
	.spot-dot {
		fill: var(--nx-bg);
		stroke: var(--nx-blue);
		stroke-width: 1.2;
		stroke-dasharray: 2 1.5;
	}
	.spot:hover .spot-dot {
		stroke-dasharray: none;
		fill: color-mix(in srgb, var(--nx-blue) 20%, var(--nx-bg));
	}
	.spot-plus {
		font-size: 8px;
		fill: var(--nx-blue);
		font-weight: 700;
	}
	.ghost {
		position: fixed;
		z-index: 50;
		pointer-events: none;
		padding: 2px 8px;
		border: 1px solid var(--nx-accent);
		border-radius: 3px;
		background: color-mix(in srgb, var(--nx-accent) 16%, var(--nx-bg));
		color: var(--nx-ui-ink);
		font-family: var(--nx-mono);
		font-size: 11px;
	}
</style>
