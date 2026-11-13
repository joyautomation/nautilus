<script lang="ts">
	// Frames the FBD viewport. Must live inside <SvelteFlow> for flow
	// context. Also the FBD half of the zoom keys Ladder/SFC get from
	// ZoomPane: Ctrl+= / Ctrl+- / Ctrl+0 (fit).
	//
	// - A new diagram (frameKey: another file, diff in/out) opens on
	//   viewFit.openViewport: fitted while that stays readable, else at
	//   READABLE_ZOOM on the diagram's top-left (#79).
	// - A structural edit (new/removed nodes) leaves the camera alone — no
	//   jump mid-edit — except to pan an added element into view when it
	//   landed off-screen.
	// - Ctrl+0 and the corner fit button still fit everything.
	import { useSvelteFlow, getViewportForBounds } from '@xyflow/svelte';
	import { openViewport, revealViewport } from './viewFit';

	let {
		structureKey,
		frameKey = '',
		centre = $bindable(),
		toFlow = $bindable()
	}: {
		structureKey: string;
		/** Changes when the diagram is a different one (file, diff mode):
		 * the opening frame is chosen again. */
		frameKey?: string;
		centre?: () => { x: number; y: number } | null;
		/** Screen (client) point → canvas point. */
		toFlow?: (p: { x: number; y: number }) => { x: number; y: number };
	} = $props();
	const { fitView, zoomIn, zoomOut, screenToFlowPosition, getNodesBounds, getNodes, getViewport, setViewport } = useSvelteFlow();
	const pane = () => document.querySelector('.svelte-flow')?.getBoundingClientRect() ?? null;
	// The canvas point at the middle of the visible pane (where a new
	// palette chip should land).
	centre = () => {
		const r = pane();
		return r ? screenToFlowPosition({ x: r.left + r.width / 2, y: r.top + r.height / 2 }) : null;
	};
	toFlow = (p) => screenToFlowPosition(p);

	let lastFrame: string | undefined;
	let lastIds = new Set<string>();
	// Whether this frame has had its opening viewport. A file that fails to
	// parse when it opens has no nodes to frame; its first model must still
	// get one even though the frame key did not change.
	let framed = false;

	// Node sizes arrive a frame or two after the nodes do: wait for them
	// (a bounds with no size), but never forever.
	function whenMeasured(ids: string[] | null, fn: () => void, tries = 12) {
		requestAnimationFrame(() => {
			const ns = ids ?? getNodes().map((n) => n.id);
			const b = ns.length ? getNodesBounds(ns) : null;
			if (b && b.width > 0 && b.height > 0) fn();
			else if (tries > 0) whenMeasured(ids, fn, tries - 1);
		});
	}

	function openFrame() {
		const r = pane();
		const ids = getNodes().map((n) => n.id);
		if (!r || !ids.length) return;
		framed = true;
		const v = openViewport(getNodesBounds(ids), { width: r.width, height: r.height }, { fit: getViewportForBounds });
		void setViewport({ x: v.x, y: v.y, zoom: v.zoom });
	}

	function reveal(added: string[]) {
		const r = pane();
		if (!r) return;
		const v = revealViewport(getViewport(), getNodesBounds(added), { width: r.width, height: r.height });
		if (v) void setViewport(v);
	}

	$effect(() => {
		const key = structureKey;
		const frame = frameKey;
		const ids = new Set(key ? key.split('|') : []);
		const added = [...ids].filter((id) => !lastIds.has(id));
		if (frame !== lastFrame) framed = false;
		const first = !framed && ids.size > 0;
		lastIds = ids;
		lastFrame = frame;
		if (first) whenMeasured(null, openFrame);
		else if (added.length) whenMeasured(added, () => reveal(added));
	});

	function onkeydown(ev: KeyboardEvent) {
		if (!(ev.ctrlKey || ev.metaKey) || ev.altKey) return;
		// The canvas has focus (a node, the pane) — or nothing does; never
		// while typing in a field or a panel outside the flow.
		const ae = document.activeElement;
		if (ae && ae !== document.body && !ae.closest('.svelte-flow')) return;
		if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA')) return;
		let acted = true;
		if (ev.key === '=' || ev.key === '+' || ev.code === 'NumpadAdd') void zoomIn();
		else if (ev.key === '-' || ev.key === '_' || ev.code === 'NumpadSubtract') void zoomOut();
		else if (ev.key === '0' || ev.code === 'Numpad0') void fitView({ padding: 0.1 });
		else acted = false;
		if (acted) {
			// Keep VS Code's window zoom out of it.
			ev.preventDefault();
			ev.stopPropagation();
		}
	}
</script>

<!-- capture: ahead of any bubble-phase listener on window (VS Code's
     keybinding forwarder included) -->
<svelte:window onkeydowncapture={onkeydown} />
