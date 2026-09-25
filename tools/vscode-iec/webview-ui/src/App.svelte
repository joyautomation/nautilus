<script lang="ts">
	// The FBD diagram editor: renders the Go model on Svelte Flow, and turns
	// every gesture into a structural op the extension pipes through
	// `naut fbd edit`. Layout is auto (banded, from topology) except for
	// nodes the user has dragged — those pin to the (* @layout *) block via
	// setLayout ops. Diff mode overlays two models read-only.
	import {
		SvelteFlow,
		Background,
		MiniMap,
		Controls,
		type Node,
		type Edge,
		type Connection
	} from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import FbdNode from './FbdNode.svelte';
	import FbdEdge from './FbdEdge.svelte';
	import FitController from './FitController.svelte';
	import Palette from './Palette.svelte';
	import VarsPanel from './VarsPanel.svelte';
	import FloatEditor from './FloatEditor.svelte';
	import InstancePanel from './InstancePanel.svelte';
	import LadderView from './LadderView.svelte';
	import { diffLd, normalizeLd, type LdElement, type LdModel, type RungStatus } from './ladder';
	import SfcView from './SfcView.svelte';
	import { diffSfc, normalizeSfc, type SfcModel } from './sfc';
	import { layout, normalizeFbd, type FbdModel, type VarDecl } from './layout';
	import { mergeDiff } from './diff';
	import { vscode, postOp, pouFromFile, setSeedPou, withSeed } from './vscodeApi';
	import { setRects, updateRect } from './diagState.svelte';
	import { live, setLive, setVarBounds } from './liveState.svelte';
	import { readClip, typingTarget, writeClip } from './clipboard';
	import ShortcutHelp from './ShortcutHelp.svelte';
	import { FBD_SHORTCUTS, LD_SHORTCUTS, SFC_SHORTCUTS, hintLine } from './shortcuts';
	import ZoomPane from './ZoomPane.svelte';
	import { loadViewState, saveViewState } from './viewState';
	import { themeColorMode } from './themeMode.svelte';

	const nodeTypes = { fbd: FbdNode };
	const edgeTypes = { fbd: FbdEdge };

	let nodes = $state.raw<Node[]>([]);
	let edges = $state.raw<Edge[]>([]);
	let ldModel = $state<LdModel | null>(null);
	let ldStatus = $state<Record<string, RungStatus>>({});
	let sfcModel = $state<SfcModel | null>(null);
	// Which editor this panel is. Set by every model message and by an
	// error that says its language, so a broken FIRST load of a .ld/.sfc
	// never falls back to FBD chrome.
	type Mode = 'fbd' | 'ld' | 'sfc';
	let mode = $state<Mode>('fbd');
	// An L5X export renders as ladder but is never editable.
	let readOnly = $state(false);
	// A whitespace-only file: no POU yet. The first op seeds a PROGRAM
	// skeleton named after the file (see vscodeApi.setSeedPou).
	let blank = $state(false);
	let seedName = $state('Main');
	// The controller-sync verdict pushed by the extension's status poll —
	// 'differs'/'edit' render a clickable pill in the toolbar.
	let syncState = $state('unknown');
	let title = $state('FBD');
	let hint = $state(true);
	const shortcutGroups = $derived(mode === 'ld' ? LD_SHORTCUTS : mode === 'sfc' ? SFC_SHORTCUTS : FBD_SHORTCUTS);
	const hintText = $derived(hintLine(shortcutGroups));
	let diffing = $state(false);
	let error = $state('');
	let structureKey = $state('');
	let paletteOpen = $state(false);
	let varsOpen = $state(false);
	let varList = $state<VarDecl[]>([]);
	let usedNames = $state(new Set<string>());
	let hasPins = $state(false);
	let selectedCount = $state(0);
	let knownIds = new Set<string>();
	type Diag = { line: number; message: string; severity: 'error' | 'warning' };
	let diags = $state<Diag[]>([]);
	let problemCount = $state(0);
	let problemTip = $state('');
	let lastModel: FbdModel | null = null;
	// Everything show()/render() touches MUST be declared above the
	// saved-state restore below (`if (saved) show(saved)`), which runs
	// during mount: a `let` further down is still in its TDZ then, the
	// ReferenceError aborts the mount before `ready` is posted, and a
	// restored panel (Reload Webviews / Reload Window) stays blank forever.
	// Node positions as last rendered — arrow-key moves persist only the
	// nodes that actually moved.
	let placedAt = new Map<string, { x: number; y: number }>();
	let arrowTimer: ReturnType<typeof setTimeout> | undefined;
	// The selection (for Ctrl+C) and the source the model came from (the
	// host sends it with every model; the clipboard snapshot).
	let selectedIds: string[] = [];
	let fbdSource: string | undefined;

	// The FB instance inspector: which called instance's live data is open.
	let inspect = $state<{ name: string; type: string; ins: string[]; outs: string[] } | null>(null);

	// The floating in-place editor (constants, renames, comments) — all the
	// commit/cancel/suggestion mechanics live in FloatEditor.
	let editor = $state<FloatEditor | null>(null);
	// Ladder retags also offer the project's nautilus.yaml tags the file
	// doesn't declare yet — picking one leaves the palette's "declare"
	// offer to add it to VAR_EXTERNAL.
	const tagItems = $derived.by(() => {
		const items = varList.map((v) => ({ name: v.name, detail: v.type }));
		if (mode !== 'ld' || !ldModel?.tags) return items;
		const have = new Set(varList.map((v) => v.name.toLowerCase()));
		for (const t of ldModel.tags) {
			if (!have.has(t.name.toLowerCase())) items.push({ name: t.name, detail: `${t.type ?? ''} · manifest`.trim() });
		}
		return items;
	});

	// "Used" for the ladder = referenced by any rung: contact/coil operands
	// (accessor bases), fb instances, and identifiers inside argument lists.
	function collectLdUsed(m: LdModel): Set<string> {
		const used = new Set<string>();
		const walk = (els: LdElement[]) => {
			for (const e of els) {
				const base = /^([A-Za-z_][A-Za-z0-9_]*)/.exec(e.ref ?? '');
				if (base) used.add(base[1].toLowerCase());
				if (e.inst) used.add(e.inst.toLowerCase());
				for (const t of e.args?.match(/[A-Za-z_][A-Za-z0-9_]*/g) ?? []) used.add(t.toLowerCase());
				for (const leg of e.legs ?? []) walk(leg);
			}
		};
		for (const r of m.rungs ?? []) {
			walk(r.elements);
			walk(r.coils);
		}
		return used;
	}

	// "Used" for the SFC vars panel = every identifier read/written by an
	// action association's target, a transition condition, or an action
	// body — the header + logic together (VarsPanel itself can't tell tags
	// from step/action names, but it only marks unreferenced tags "unused";
	// a false negative here just costs the badge, never breaks anything).
	function collectSfcUsed(m: SfcModel): Set<string> {
		const used = new Set<string>();
		const words = (s: string) => {
			for (const w of s.match(/[A-Za-z_][A-Za-z0-9_]*/g) ?? []) used.add(w.toLowerCase());
		};
		for (const s of m.steps ?? []) for (const a of s.actions ?? []) used.add(a.target.toLowerCase());
		for (const t of m.trans ?? []) words(t.cond ?? '');
		for (const a of m.actions ?? []) words(a.body ?? '');
		return used;
	}

	function requestInput(
		init: string,
		at: { x: number; y: number; w: number },
		commit: (v: string) => void,
		opts?: { multiline?: boolean; suggest?: 'tags' | 'types' | 'functions' }
	) {
		editor?.open({ init, at, commit, ...opts });
	}

	function render(model: FbdModel, isDiff: boolean) {
		if (!isDiff) lastModel = model;
		const { placed, edges: modelEdges, laneIdx } = layout(model);
		const editable = !isDiff;
		// Join the compiler's squiggles onto nodes by source line — the same
		// message the text editor shows, as a badge + tooltip on the block.
		const diagsByLine = new Map<number, Diag[]>();
		if (!isDiff) {
			for (const d of diags) {
				(diagsByLine.get(d.line) ?? diagsByLine.set(d.line, []).get(d.line)!).push(d);
			}
		}
		problemCount = diags.length;
		problemTip = diags.map((d) => `line ${d.line}: ${d.message}`).join('\n');
		hasPins = model.nodes.some((n) => n.x !== undefined && n.y !== undefined);
		if (!isDiff) {
			varList = model.vars ?? [];
			// Indexed chips (TempHist[2]) need declared array bounds to
			// resolve their live values.
			setVarBounds(varList);
			// Referenced = it became a diagram element (chip, coil, FB instance).
			usedNames = new Set(
				model.nodes
					.filter((n) => n.kind === 'input' || n.kind === 'coil' || n.kind === 'fb')
					.map((n) => n.label.split('.')[0].toLowerCase())
			);
		}
		// VAR_EXTERNAL names (lowercased): chips reading one that has no tag
		// on the live controller warn — that read faults the scan.
		const extNames = new Set(
			(model.vars ?? [])
				.filter((v) => v.section === 'VAR_EXTERNAL')
				.map((v) => v.name.toLowerCase())
		);
		nodes = placed.map((n) => ({
			id: n.id,
			type: 'fbd',
			position: { x: n.x, y: n.y },
			data: {
				n,
				problems: n.line ? (diagsByLine.get(n.line) ?? []) : [],
				editable,
				extNames,
				requestInput,
				onInspect: (inst: { name: string; type: string; ins: string[]; outs: string[] }) => {
					inspect = inst;
					paletteOpen = varsOpen = false;
				},
				onEdit: (a: {
					type: 'setLiteral' | 'rename' | 'setComment' | 'retarget';
					node: string;
					value: string;
					declareType?: string;
				}) => {
					if (a.type === 'setLiteral') postOp({ type: 'setLiteral', node: a.node, value: a.value });
					else if (a.type === 'rename') postOp({ type: 'rename', node: a.node, newName: a.value });
					else if (a.type === 'setComment') postOp({ type: 'setComment', node: a.node, text: a.value });
					// retarget: value is the tag name; declareType ("Name : TYPE"
					// in the editor) declares it in the same op.
					else
						postOp({
							type: 'retarget',
							node: a.node,
							newName: a.value,
							value: a.declareType,
							text: a.declareType ? 'VAR_EXTERNAL' : undefined
						});
				}
			},
			draggable: editable,
			connectable: editable,
			selectable: true,
			deletable:
					editable &&
					(n.id.startsWith('b:w.') ||
						n.id.startsWith('c:') ||
						n.id.startsWith('f:') ||
						n.id.startsWith('cm:') ||
						n.id.startsWith('g:'))
		}));
		const srcWire = new Map(placed.map((n) => [n.id, !!n.wire]));
		edges = modelEdges.map((e, i) => ({
			id: `${e.from}|${e.fromPin ?? ''}|${e.to}|${e.toPin ?? ''}|${i}`,
			type: 'fbd',
			source: e.from,
			sourceHandle: e.fromPin ?? '',
			target: e.to,
			targetHandle: e.toPin ?? '',
			selectable: editable,
			data: {
				e,
				lane: laneIdx.get(e),
				editable,
				showWireLabel: !!e.wire && !srcWire.get(e.from)
			}
		}));
		setRects(placed.map((n) => [n.id, { x: n.x, y: n.y, w: n.w, h: n.h }]));
		placedAt = new Map(placed.map((n) => [n.id, { x: n.x, y: n.y }]));
		knownIds = new Set(placed.map((n) => n.id));
		structureKey = placed
			.map((n) => n.id)
			.sort()
			.join('|');
		diffing = isDiff;
	}

	type Msg =
		| { type: 'model'; model: FbdModel; title?: string; source?: string }
		| { type: 'diff'; base: FbdModel; head: FbdModel; title?: string }
		| { type: 'ldModel'; model: LdModel; title?: string; readOnly?: boolean }
		| { type: 'ldDiff'; base: LdModel; head: LdModel; title?: string }
		| { type: 'sfcModel'; model: SfcModel; title?: string }
		| { type: 'sfcDiff'; base: SfcModel; head: SfcModel; title?: string }
		| { type: 'error'; message: string; title?: string; lang?: Mode };

	// Track the blank-file state from a live (non-diff) model.
	function noteBlank(isBlank: boolean | undefined, file: string | undefined) {
		blank = !!isBlank;
		seedName = pouFromFile(file);
		setSeedPou(blank ? seedName : undefined);
	}

	function show(msg: Msg) {
		if (msg.type === 'model') {
			mode = 'fbd';
			readOnly = false;
			fbdSource = msg.source;
			noteBlank(msg.model.blank, msg.title);
			title = (msg.model.name ? msg.model.name + ' — ' : '') + (msg.title ?? '');
			error = '';
			ldModel = null;
			sfcModel = null;
			render(normalizeFbd(msg.model), false);
		} else if (msg.type === 'diff') {
			mode = 'fbd';
			title = (msg.head.name ? msg.head.name + ' — ' : '') + (msg.title ?? '');
			error = '';
			ldModel = null;
			sfcModel = null;
			render(mergeDiff(normalizeFbd(msg.base), normalizeFbd(msg.head)), true);
		} else if (msg.type === 'ldModel') {
			mode = 'ld';
			readOnly = !!msg.readOnly;
			noteBlank(msg.model.blank, msg.title);
			// Ladder mode: canonical rung layout, no flow canvas. Array
			// bounds still come from the header so indexed contacts resolve,
			// and the header vars feed the SAME vars panel + tag-suggest
			// dropdowns the FBD editor uses.
			title = (msg.model.name ? msg.model.name + ' — ' : '') + (msg.title ?? '');
			error = '';
			sfcModel = null;
			varList = (msg.model.vars ?? []) as VarDecl[];
			setVarBounds(varList);
			usedNames = collectLdUsed(msg.model);
			ldModel = normalizeLd(msg.model);
			ldStatus = {};
			diffing = false;
			problemCount = diags.length;
			problemTip = diags.map((d) => `line ${d.line}: ${d.message}`).join('\n');
		} else if (msg.type === 'ldDiff') {
			// Ladder diff: base overlaid onto head at rung granularity —
			// removed rungs splice back in ghosted, changed/added get status
			// colors. Read-only until the next ldModel arrives.
			mode = 'ld';
			title = msg.title ?? title;
			error = '';
			sfcModel = null;
			const d = diffLd(normalizeLd(msg.base), normalizeLd(msg.head));
			ldModel = d.model;
			ldStatus = d.status;
			diffing = true;
			problemCount = 0;
		} else if (msg.type === 'sfcModel') {
			// SFC mode: the pure layoutSfc pass (sfc.ts) derives step/
			// transition geometry from topology; header vars feed the same
			// vars panel FBD/LD use.
			mode = 'sfc';
			readOnly = false;
			noteBlank(msg.model.blank, msg.title);
			title = (msg.model.name ? msg.model.name + ' — ' : '') + (msg.title ?? '');
			error = '';
			ldModel = null;
			varList = (msg.model.vars ?? []) as VarDecl[];
			setVarBounds(varList);
			usedNames = collectSfcUsed(msg.model);
			sfcModel = normalizeSfc(msg.model);
			diffing = false;
			problemCount = diags.length;
			problemTip = diags.map((d) => `line ${d.line}: ${d.message}`).join('\n');
		} else if (msg.type === 'sfcDiff') {
			// SFC diff: diffSfc (sfc.ts) overlays base onto head at
			// step/transition/action/comment granularity — added/removed/
			// changed marks, rendered by SfcView with the same
			// --nx-added/removed/changed palette FBD/Ladder diffs use.
			// Read-only until the next plain sfcModel arrives.
			mode = 'sfc';
			title = (msg.head.name ? msg.head.name + ' — ' : '') + (msg.title ?? '');
			error = '';
			ldModel = null;
			varList = (msg.head.vars ?? []) as VarDecl[];
			setVarBounds(varList);
			sfcModel = diffSfc(normalizeSfc(msg.base), normalizeSfc(msg.head));
			diffing = true;
			problemCount = 0;
		} else {
			// The error names its language (ld/sfc hosts), so a file that
			// fails to parse on FIRST open keeps its own editor's chrome.
			if (msg.lang) mode = msg.lang;
			title = msg.title ?? title;
			error = msg.message;
		}
	}

	// The ld/sfc op channels; a blank file's ops carry the seed POU name.
	const postLd = (op: Record<string, unknown>) => vscode.postMessage({ type: 'ldEdit', op: withSeed(op) });
	const postSfc = (op: Record<string, unknown>) => vscode.postMessage({ type: 'sfcEdit', op: withSeed(op) });
	function initBlank() {
		if (mode === 'ld') postLd({ type: 'init' });
		else if (mode === 'sfc') postSfc({ type: 'init' });
		else postOp({ type: 'init' });
	}

	window.addEventListener('message', (ev) => {
		const msg = ev.data as
			| Msg
			| { type: 'diagnostics'; diags?: Diag[] }
			| { type: 'liveValues'; enabled?: boolean; fresh?: boolean; values?: Record<string, unknown> }
			| { type: 'syncState'; state?: string };
		if (!msg?.type) return;
		if (msg.type === 'syncState') {
			syncState = msg.state ?? 'unknown';
			return;
		}
		if (msg.type === 'liveValues') {
			// Store-only update: FbdNode pills react directly, no node rebuild.
			setLive({ enabled: !!msg.enabled, fresh: !!msg.fresh, values: msg.values ?? {} });
			return;
		}
		if (msg.type === 'diagnostics') {
			diags = msg.diags ?? [];
			if (ldModel || sfcModel) {
				// Ladder/SFC mode: LadderView/SfcView join squiggles onto
				// rungs/steps themselves; just keep the toolbar pill current.
				problemCount = diags.length;
				problemTip = diags.map((d) => `line ${d.line}: ${d.message}`).join('\n');
			} else if (!diffing && lastModel) {
				render(lastModel, false);
			}
			return;
		}
		if (msg.type !== 'error') saveViewState({ msg });
		show(msg);
	});
	const viewState = loadViewState();
	const saved = viewState.msg as Msg | undefined;
	if (saved) show(saved);
	// The handshake: until this lands, the host holds its messages (a model
	// or live frame posted before the bundle's listener exists is lost),
	// then replays the latest of each. Sent again by every reload.
	vscode.postMessage({ type: 'ready' });

	// Ladder/SFC zoom, per panel (webview state, never the source). Unset =
	// this panel was never zoomed: ZoomPane fits on first render.
	let ldZoom = $state(viewState.zoom?.ld ?? 1);
	let sfcZoom = $state(viewState.zoom?.sfc ?? 1);
	const zoomSaved = { ld: viewState.zoom?.ld !== undefined, sfc: viewState.zoom?.sfc !== undefined };
	let zoomTouched = false;
	$effect(() => {
		const z = { ld: ldZoom, sfc: sfcZoom };
		// Skip the initial run: only a real zoom (or the first fit) persists.
		if (!zoomTouched) {
			zoomTouched = true;
			return;
		}
		if (z.ld !== 1 || zoomSaved.ld) zoomSaved.ld = true;
		if (z.sfc !== 1 || zoomSaved.sfc) zoomSaved.sfc = true;
		saveViewState({ zoom: { ld: zoomSaved.ld ? z.ld : undefined, sfc: zoomSaved.sfc ? z.sfc : undefined } });
	});
	const injected = window.__MODEL__ as FbdModel | undefined;
	if (injected) show({ type: 'model', model: injected, title: 'harness' });
	// Browser-harness hook for the SFC webview (mirrors __MODEL__ above):
	// window.__SFC_MODEL__ renders directly, and every op it posts lands on
	// window.__POSTED__ via vscodeApi's headless fallback.
	const injectedSfc = (window as unknown as { __SFC_MODEL__?: SfcModel }).__SFC_MODEL__;
	if (injectedSfc) show({ type: 'sfcModel', model: injectedSfc, title: 'harness' });
	const injectedLd = (window as unknown as { __LD_MODEL__?: LdModel }).__LD_MODEL__;
	if (injectedLd) show({ type: 'ldModel', model: injectedLd, title: 'harness' });

	// ── gestures → ops ──────────────────────────────────────────────────────
	function onconnect(c: Connection) {
		// Dragging output→input rewires that input (or wires an unwired FB
		// pin); dropping on an extensible block's "+" pin appends an input.
		// We never mutate edges locally: the op round-trips through the text
		// and the re-render brings the new wiring back.
		if (c.targetHandle === '+') {
			postOp({
				type: 'addInput',
				node: c.target,
				source: c.source,
				sourcePin: c.sourceHandle ?? ''
			});
			return;
		}
		postOp({
			type: 'rewire',
			to: c.target,
			toPin: c.targetHandle ?? '',
			source: c.source,
			sourcePin: c.sourceHandle ?? ''
		});
	}

	function onbeforedelete({ nodes: sel, edges: selEdges }: { nodes: Node[]; edges: Edge[] }) {
		// The whole selection is ONE op: comment ids are ordinals (cm:N), so
		// per-node ops would each resolve against text the previous delete
		// already renumbered — select notes A,B of A/B/C and A,C would go.
		const gone = sel.map((n) => n.id).filter((id) => id && knownIds.has(id));
		if (gone.length) postOp({ type: 'deleteNode', nodes: gone });
		const goneSet = new Set(gone);
		// A selected edge deletes as a DISCONNECT: FB pins drop their named
		// arg, extensible inputs shrink, fixed-arity pins placehold, coils
		// revert to floating ghosts. The endpoints' live positions ride
		// along so a ghost keeps its spot even if it was never dragged.
		const liveAt = (id: string) => {
			const n = nodes.find((n) => n.id === id);
			return n ? [{ node: id, x: Math.round(n.position.x), y: Math.round(n.position.y) }] : [];
		};
		// Extensible inputs are positional too (AND's IN1..INn shrink on a
		// disconnect): take each block's pins highest-first so an earlier
		// disconnect never renumbers a later one's target.
		const pinNo = (e: Edge) => Number(/(\d+)$/.exec(String(e.targetHandle ?? ''))?.[1] ?? 0);
		const ordered = [...selEdges].sort((a, b) =>
			a.target === b.target ? pinNo(b) - pinNo(a) : a.target < b.target ? -1 : 1
		);
		for (const e of ordered) {
			const d = e.data as { e?: { to: string; toPin?: string; from: string; fromPin?: string } };
			if (!d?.e) continue;
			// xyflow hands over every edge attached to a deleted node; those
			// vanish with the node's statement — a disconnect would only
			// fail ("no connection into …") once the delete has landed.
			if (goneSet.has(d.e.from) || goneSet.has(d.e.to)) continue;
			postOp({
				type: 'disconnect',
				to: d.e.to,
				toPin: d.e.toPin ?? '',
				from: d.e.from,
				fromPin: d.e.fromPin ?? '',
				entries: [...liveAt(d.e.from), ...liveAt(d.e.to)]
			});
		}
		return Promise.resolve(false); // ops re-render; never delete locally
	}

	function trackDrag(dragged: Node[]) {
		// Keep the live rect store current so feedback lanes reroute around
		// nodes as they move. Selection drags can hand us synthetic group
		// entries — only track nodes we actually rendered.
		for (const n of dragged) {
			if (!n?.id || !knownIds.has(n.id)) continue;
			const pn = (n.data as { n?: import('./layout').Placed })?.n;
			if (!pn) continue;
			updateRect(n.id, { x: n.position.x, y: n.position.y, w: pn.w, h: pn.h });
		}
	}

	function onnodedrag({ nodes: dragged }: { nodes: Node[] }) {
		trackDrag(dragged);
	}

	function onnodedragstop({ nodes: dragged }: { nodes: Node[] }) {
		trackDrag(dragged);
		// ONE batched op for the whole selection — per-node ops would race
		// each other rewriting the layout block and drop all but the last.
		// Selection drags include synthetic group entries; pin only the nodes
		// that exist in the model.
		const entries = dragged
			.filter((n) => n?.id && knownIds.has(n.id))
			.map((n) => ({
				node: n.id,
				x: Math.round(n.position.x),
				y: Math.round(n.position.y)
			}));
		if (entries.length === 0) return;
		postOp({ type: 'setLayout', entries });
	}

	// Arrow keys move the selection (xyflow's keyboard a11y) — persist it
	// like a drag, once the key presses settle, or the next render snaps
	// the nodes back. Only nodes that actually moved are pinned.
	// (placedAt / arrowTimer are declared with the other state up top.)
	function persistKeyboardMove() {
		const entries = nodes
			.filter((n) => n.selected && knownIds.has(n.id))
			.filter((n) => {
				const at = placedAt.get(n.id);
				return !at || Math.round(n.position.x) !== Math.round(at.x) || Math.round(n.position.y) !== Math.round(at.y);
			})
			.map((n) => ({ node: n.id, x: Math.round(n.position.x), y: Math.round(n.position.y) }));
		if (entries.length) postOp({ type: 'setLayout', entries });
	}

	function onselectionchange({ nodes: sel }: { nodes: Node[]; edges: Edge[] }) {
		selectedCount = sel.length;
		selectedIds = sel.map((n) => n.id).filter(Boolean);
	}

	// ── copy / cut / paste / select all ────────────────────────────────────
	// Ctrl+C captures the selection's ids plus the source they resolve
	// against (the host sends it with every model); Ctrl+V posts ONE
	// duplicate op — Go copies the statements behind the ids with fresh
	// names and keeps references between them consistent. Pasting into the
	// file they came from, uncut, duplicates in place; otherwise (after a
	// cut, or into another .fbd via the system clipboard) the snapshot is
	// the source and the copies land at the end of the FBD block.
	type FbdClip = { nodes: string[]; source?: string; cut?: boolean };
	// (selectedIds / fbdSource are declared with the other state up top.)
	function copySelection(cut = false): boolean {
		const ids = selectedIds.filter((id) => knownIds.has(id));
		if (!ids.length) return false;
		writeClip('fbd', { nodes: ids, source: fbdSource, cut } satisfies FbdClip);
		return true;
	}
	function cutSelection(): boolean {
		if (!copySelection(true)) return false;
		// The same batched delete the Del key posts (onbeforedelete).
		postOp({ type: 'deleteNode', nodes: selectedIds.filter((id) => knownIds.has(id)) });
		return true;
	}
	async function pasteClip() {
		const clip = await readClip<FbdClip>('fbd');
		if (!clip?.nodes?.length) return;
		const inPlace = !clip.source || (clip.source === fbdSource && !clip.cut);
		postOp(
			inPlace
				? { type: 'duplicate', nodes: clip.nodes }
				: { type: 'duplicate', nodes: clip.nodes, text: clip.source, keepRefs: clip.cut || undefined }
		);
	}
	function selectAll() {
		nodes = nodes.map((n) => ({ ...n, selected: true }));
		selectedIds = nodes.map((n) => n.id);
		selectedCount = selectedIds.length;
	}
	function onkeydown(ev: KeyboardEvent) {
		if (diffing || mode !== 'fbd') return;
		// Typing in any editor (float editor, palette field) is never a
		// canvas shortcut.
		if (typingTarget(ev)) return;
		if (ev.key.startsWith('Arrow') && !ev.ctrlKey && !ev.metaKey && selectedIds.length) {
			clearTimeout(arrowTimer);
			arrowTimer = setTimeout(persistKeyboardMove, 350);
			return;
		}
		if (!(ev.ctrlKey || ev.metaKey) || ev.altKey) return;
		const k = ev.key.toLowerCase();
		let acted = false;
		if (k === 'c') acted = copySelection();
		else if (k === 'x') acted = cutSelection();
		else if (k === 'v') {
			void pasteClip();
			acted = true;
		} else if (k === 'a') {
			selectAll();
			acted = true;
		}
		if (acted) ev.preventDefault();
	}
</script>

<div class="host" class:diffing>
	<div class="bar">
		<span class="title">{title}</span>
		{#if diffing}
			<span class="legend" class:ld={!!ldModel}>
				<span><i class="sw added"></i>added</span>
				<span><i class="sw removed"></i>removed</span>
				<span><i class="sw changed"></i>changed</span>
			</span>
		{:else if readOnly}
			<span
				class="ropill"
				title="An L5X is a Rockwell export: it renders as ladder here, but edits belong in Logix Designer or in the nautilus source it was generated from."
			>read-only · Logix export</span>
		{:else if hint}
			<!-- Generated from the same table as the "?" popover; the full
			     text is the tooltip when a narrow pane cuts it off. -->
			<span class="hint" title={hintText}>{hintText}</span>
		{/if}
		<span class="spacer"></span>
		{#if problemCount > 0 && !diffing}
			<span class="problems" title={problemTip}>{problemCount} problem{problemCount === 1 ? '' : 's'}</span>
		{/if}
		{#if selectedCount > 0}
			<span class="selcount">{selectedCount} selected</span>
		{/if}
		{#if diffing}
			<button title="Leave the diff and return to the live editable view" onclick={() => vscode.postMessage({ type: 'exitDiff' })}>✕ exit diff</button>
		{/if}
		{#if !diffing}
			{#if syncState === 'differs' || syncState === 'edit'}
				<button
					class="syncpill"
					class:edit={syncState === 'edit'}
					title={syncState === 'differs'
						? 'The controller is running a DIFFERENT program than this file — click for a visual diff against live'
						: 'The controller runs your latest download (matches this file) but not what it booted with — a restart reverts. Click to diff.'}
					onclick={() => vscode.postMessage({ type: 'diffLive' })}
				>{syncState === 'differs' ? '≠ controller' : 'online edit'}</button>
			{/if}
			{#if live.seen}
				<!-- same toggle as the text editor's status bar item -->
				<button
					class="livepill"
					class:on={live.enabled && live.fresh}
					class:offline={live.enabled && !live.fresh}
					title={live.enabled
						? live.fresh
							? 'Streaming live values from the controller — click to disable'
							: 'Live values enabled but no frames arriving — is the controller running? Click to disable'
						: 'Live values are off — click to enable'}
					onclick={() => vscode.postMessage({ type: 'toggleLive' })}
				>{live.enabled ? (live.fresh ? '● live' : '◌ offline') : '○ live off'}</button>
			{/if}
			{#if hasPins && mode === 'fbd'}
				<button title="Clear all pinned positions (back to full auto-layout)" onclick={() => postOp({ type: 'clearLayout' })}>auto layout</button>
			{/if}
			<ShortcutHelp groups={shortcutGroups} />
			<button title="All header declarations, including ones the logic doesn't reference yet" onclick={(e) => { e.stopPropagation(); varsOpen = !varsOpen; paletteOpen = false; }}>vars</button>
			{#if mode === 'fbd'}
				<button title="Insert an instruction" onclick={(e) => { e.stopPropagation(); paletteOpen = !paletteOpen; varsOpen = false; }}>+ add</button>
			{/if}
		{/if}
	</div>
	{#if error}
		<div class="error">{error}</div>
	{/if}
	{#if blank && !diffing && !readOnly && !error}
		<div class="blank">
			<span>Empty file — the first edit writes a <code>PROGRAM {seedName}</code> skeleton{mode === 'ld' ? ' (start with “+ rung”)' : mode === 'sfc' ? ' (start with “+ step”)' : ' (start with “+ add”)'}.</span>
			<button title="Write the PROGRAM skeleton now" onclick={initBlank}>initialize</button>
		</div>
	{/if}
	{#if ldModel}
		<ZoomPane bind:zoom={ldZoom} autoFit={!zoomSaved.ld} fitAxis="width" stale={!!error} label="ladder">
			<LadderView
				model={ldModel}
				editable={!diffing && !readOnly}
				diags={diffing ? [] : diags}
				status={ldStatus}
				showLive={!diffing}
				zoom={ldZoom}
				onOp={postLd}
				onTrace={(msg) => vscode.postMessage({ type: 'ldTrace', msg })}
				{requestInput}
			/>
		</ZoomPane>
	{:else if sfcModel}
		<ZoomPane bind:zoom={sfcZoom} autoFit={!zoomSaved.sfc} stale={!!error} label="SFC">
			<SfcView
				model={sfcModel}
				editable={!diffing}
				diags={diffing ? [] : diags}
				showLive={!diffing}
				zoom={sfcZoom}
				onOp={postSfc}
				{requestInput}
			/>
		</ZoomPane>
	{:else if mode !== 'fbd'}
		<!-- a ladder/SFC file whose first parse failed: the error above
		     says why; no FBD canvas (or its "+ add") in its place -->
		<div class="flow"></div>
	{:else}
	<div class="flow" class:stale={!!error}>
		<SvelteFlow
			bind:nodes
			bind:edges
			{nodeTypes}
			{edgeTypes}
			{onconnect}
			{onbeforedelete}
			{onnodedrag}
			{onnodedragstop}
			{onselectionchange}
			zoomOnDoubleClick={false}
			fitView
			minZoom={0.15}
			colorMode={themeColorMode.mode}
			deleteKey={['Delete', 'Backspace']}
			proOptions={{ hideAttribution: true }}
		>
			<Background />
			<MiniMap pannable zoomable />
			<Controls />
			<FitController {structureKey} />
		</SvelteFlow>
	</div>
	{/if}
	<Palette bind:open={paletteOpen} vars={varList} />
	<VarsPanel
		bind:open={varsOpen}
		vars={varList}
		used={usedNames}
		readonly={readOnly || diffing}
		onDeclare={mode === 'ld'
			? (name, type, section) => postLd({ type: 'declareVar', name, varType: type, section })
			: mode === 'sfc'
				? (name, type, section) => postSfc({ type: 'declareVar', name, varType: type, section })
				: undefined}
		onDelete={mode === 'ld'
			? (name) => postLd({ type: 'deleteVar', name })
			: mode === 'sfc'
				? (name) => postSfc({ type: 'deleteVar', name })
				: undefined}
	/>
	{#if inspect}
		<InstancePanel inst={inspect} onclose={() => (inspect = null)} />
	{/if}
	<FloatEditor bind:this={editor} {tagItems} />
</div>

<svelte:window onclick={() => { paletteOpen = false; varsOpen = false; inspect = null; }} {onkeydown} />

<style>
	.host {
		height: 100vh;
		display: flex;
		flex-direction: column;
		background: var(--nx-bg);
		color: var(--nx-ui-ink);
		font-family: var(--nx-font);
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 4px 10px;
		font-size: 12px;
		border-bottom: 1px solid var(--nx-border);
		user-select: none;
	}
	.bar .title {
		font-weight: 600;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.bar .hint {
		font-size: 11px;
		color: var(--nx-muted);
		/* The hint yields space first — it must never squeeze the buttons
		   into wrapping their labels. */
		min-width: 0;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.bar .spacer {
		flex: 1;
	}
	.bar button {
		background: transparent;
		color: var(--nx-ui-ink);
		border: 1px solid var(--nx-border);
		border-radius: 3px;
		padding: 1px 8px;
		height: 22px;
		cursor: pointer;
		font-size: 12px;
		white-space: nowrap;
		flex-shrink: 0;
	}
	.bar button:hover {
		background: var(--nx-hover);
	}
	.bar button.livepill {
		border-radius: 999px;
		color: var(--nx-muted);
	}
	.bar button.syncpill {
		border-radius: 999px;
		color: var(--nx-warn);
		border-color: var(--nx-warn);
		font-weight: 600;
	}
	.bar button.syncpill.edit {
		color: var(--nx-accent);
		border-color: var(--nx-accent);
	}
	.bar button.livepill.on {
		color: var(--nx-ok);
		border-color: var(--nx-ok);
	}
	.bar button.livepill.offline {
		color: var(--nx-warn);
		border-color: var(--nx-warn);
	}
	.legend {
		display: inline-flex;
		gap: 10px;
		font-size: 11px;
	}
	.legend .sw {
		display: inline-block;
		width: 10px;
		height: 10px;
		border-radius: 2px;
		margin-right: 3px;
		vertical-align: -1px;
	}
	.legend .sw.added {
		background: var(--nx-added);
	}
	.legend .sw.removed {
		background: var(--nx-removed);
	}
	.legend .sw.changed {
		background: var(--nx-changed);
	}
	/* the ladder diff uses its own palette (green means power there) */
	.legend.ld .sw.added {
		background: var(--nx-ld-added);
	}
	.legend.ld .sw.changed {
		background: var(--nx-ld-changed);
	}
	.ropill {
		font-size: 11px;
		font-weight: 600;
		padding: 1px 8px;
		border-radius: 999px;
		color: var(--nx-muted);
		border: 1px solid var(--nx-border);
		white-space: nowrap;
		cursor: help;
	}
	.blank {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		font-size: 12px;
		border-bottom: 1px solid var(--nx-border);
		background: var(--nx-panel-bg);
	}
	.blank button {
		background: transparent;
		color: var(--nx-ui-ink);
		border: 1px solid var(--nx-accent);
		border-radius: 3px;
		padding: 1px 8px;
		cursor: pointer;
		font-size: 12px;
	}
	.selcount {
		font-size: 11px;
		font-weight: 600;
		padding: 1px 8px;
		border-radius: 999px;
		color: var(--nx-accent);
		border: 1px solid var(--nx-accent);
		white-space: nowrap;
		flex-shrink: 0;
	}
	.problems {
		font-size: 11px;
		font-weight: 600;
		padding: 1px 8px;
		border-radius: 999px;
		color: var(--nx-err);
		border: 1px solid var(--nx-err);
		cursor: help;
		white-space: nowrap;
		flex-shrink: 0;
	}
	.error {
		padding: 6px 10px;
		font-size: 12px;
		white-space: pre-wrap;
		font-family: var(--nx-mono);
		color: var(--nx-err);
		background: var(--nx-err-bg);
		border-bottom: 1px solid var(--nx-err);
	}
	.flow {
		flex: 1;
		min-height: 0;
	}
	.flow.stale {
		opacity: 0.45;
	}
	.diffing :global(.fbd-edge.same),
	.diffing :global(.svelte-flow__node:has(.same)) {
		opacity: 0.6;
	}
	/* xyflow chrome from the theme tokens (colorMode follows the VS Code
	   theme kind too, for anything not overridden here). */
	:global(.svelte-flow) {
		background: var(--nx-bg) !important;
		--xy-minimap-background-color: var(--nx-panel-bg);
		--xy-minimap-mask-background-color: color-mix(in srgb, var(--nx-bg) 55%, transparent);
		--xy-minimap-mask-stroke-color: var(--nx-border);
		--xy-minimap-node-background-color: color-mix(in srgb, var(--nx-ink) 38%, var(--nx-panel-bg));
		--xy-controls-button-background-color: var(--nx-panel-bg);
		--xy-controls-button-background-color-hover: var(--nx-ctl-hover);
		--xy-controls-button-color: var(--nx-ui-ink);
		--xy-controls-button-color-hover: var(--nx-ui-ink);
		--xy-controls-button-border-color: var(--nx-border);
		--xy-controls-box-shadow: var(--nx-shadow);
	}
	:global(.svelte-flow__controls) {
		border: 1px solid var(--nx-border);
	}
	:global(.svelte-flow__minimap) {
		border: 1px solid var(--nx-border);
	}
	:global(.svelte-flow__edge.selected .wirepath) {
		stroke: var(--nx-accent) !important;
		stroke-width: 2.4;
	}
	:global(.svelte-flow__connectionline path) {
		stroke: var(--nx-blue);
		stroke-dasharray: 5 3;
	}
</style>
