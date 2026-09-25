<script lang="ts">
	// Zoom / pan / fit for the hand-drawn SVG editors (Ladder, SFC) — what
	// xyflow's Controls give FBD. The pane is the scroll container; the view
	// inside marks the element that scales with `data-zoom-content` and
	// draws itself at `zoom` (SVG width/height × zoom over an unscaled
	// viewBox), so layout, scrollbars, elementFromPoint and
	// getBoundingClientRect all stay in one consistent screen space — no
	// CSS transform for hit-testing to fight. Views convert pointer deltas
	// back to diagram units by dividing by `zoom`.
	//
	// Gestures: Ctrl/Cmd+wheel and trackpad pinch (Chromium reports a pinch
	// as a ctrl+wheel) zoom around the cursor; Ctrl+= / Ctrl+- / Ctrl+0
	// (fit) while focus is inside the pane; middle-button drag pans; the
	// bottom-left buttons sit where FBD's xyflow Controls do.
	import { flushSync, onMount, type Snippet } from 'svelte';

	let {
		zoom = $bindable(1),
		autoFit = false,
		fitAxis = 'both',
		stale = false,
		label,
		children
	}: {
		zoom?: number;
		/** Fit once the content first renders (the panel has no saved zoom). */
		autoFit?: boolean;
		/** 'width': fit the widest rung to the pane (a ladder is a vertical
		 * list of rungs — fitting its height would shrink a long program to
		 * nothing); 'both': fit the whole chart. */
		fitAxis?: 'both' | 'width';
		stale?: boolean;
		label: string;
		children: Snippet;
	} = $props();

	// Room under the diagram for the bottom-left controls (15 px inset +
	// three 26 px buttons + the % row): the pane scrolls this far past the
	// content, so the lowest step or rung can always be brought up clear
	// of the controls instead of sitting under them.
	const CTL_CLEAR = 120;
	const MIN_ZOOM = 0.25;
	const MAX_ZOOM = 3;
	const STEP = 1.2;
	const AUTO_FIT_FLOOR = 0.5;
	const clamp = (z: number) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, z));

	let scroller = $state<HTMLDivElement | undefined>();
	// The pane's inner width (scrollbar excluded), as --pane-w: the views'
	// sticky palettes take it so they stay put while zoomed content
	// scrolls sideways (a sticky box as wide as the content can't stick).
	let paneW = $state(0);
	const content = () => scroller?.querySelector<HTMLElement | SVGElement>('[data-zoom-content]') ?? null;

	/** Zoom to `next`, keeping the diagram point under (cx, cy) — client
	 * coordinates — exactly where it was. */
	export function zoomAt(next: number, cx?: number, cy?: number): void {
		const z0 = zoom;
		const z1 = Math.round(clamp(next) * 1000) / 1000;
		if (z1 === z0 || !scroller) {
			zoom = z1;
			return;
		}
		const sr = scroller.getBoundingClientRect();
		const px = cx ?? sr.left + scroller.clientWidth / 2;
		const py = cy ?? sr.top + scroller.clientHeight / 2;
		const el = content();
		const r0 = el?.getBoundingClientRect();
		// The anchor in diagram units (content-relative, unscaled).
		const ux = r0 ? (px - r0.left) / z0 : 0;
		const uy = r0 ? (py - r0.top) / z0 : 0;
		flushSync(() => (zoom = z1));
		const r1 = content()?.getBoundingClientRect();
		if (!r1) return;
		scroller.scrollLeft += r1.left + ux * z1 - px;
		scroller.scrollTop += r1.top + uy * z1 - py;
	}

	export function zoomIn(): void {
		zoomAt(zoom * STEP);
	}
	export function zoomOut(): void {
		zoomAt(zoom / STEP);
	}

	/** Shrink to fit (never magnifies past 100%, never below `floor`), then
	 * show the top-left. */
	export function fit(floor = MIN_ZOOM): void {
		const el = content();
		if (!scroller || !el) return;
		const r = el.getBoundingClientRect();
		const sr = scroller.getBoundingClientRect();
		// Unscaled chrome above/left of the content (the sticky palette)
		// eats pane space the diagram can't use.
		const offTop = r.top - sr.top + scroller.scrollTop;
		const offLeft = r.left - sr.left + scroller.scrollLeft;
		const natW = Number(el.getAttribute('data-natural-w')) || r.width / zoom;
		const natH = r.height / zoom;
		if (!natW || !natH) return;
		const availW = scroller.clientWidth - offLeft - 4;
		// The clearance strip is scroll room, not diagram: fitting into
		// the pane minus it keeps a fitted chart's bottom off the controls.
		const availH = scroller.clientHeight - offTop - CTL_CLEAR - 4;
		let z = availW / natW;
		if (fitAxis === 'both') z = Math.min(z, availH / natH);
		flushSync(() => (zoom = Math.round(clamp(Math.max(floor, Math.min(1, z))) * 1000) / 1000));
		scroller.scrollLeft = 0;
		scroller.scrollTop = 0;
	}

	// Ctrl/Cmd+wheel (and pinch). Non-passive: preventDefault is what keeps
	// the page — and VS Code's own zoom — out of it.
	function onWheel(ev: WheelEvent) {
		if (!(ev.ctrlKey || ev.metaKey)) return;
		ev.preventDefault();
		// Line/page deltas (some mice) are ~16/~800px a unit.
		const unit = ev.deltaMode === 1 ? 16 : ev.deltaMode === 2 ? 800 : 1;
		const d = Math.max(-60, Math.min(60, ev.deltaY * unit));
		zoomAt(zoom * Math.exp(-d * 0.0025), ev.clientX, ev.clientY);
	}

	function onKey(ev: KeyboardEvent) {
		if (!(ev.ctrlKey || ev.metaKey) || ev.altKey) return;
		const t = ev.target as HTMLElement | null;
		if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT')) return;
		let acted = true;
		if (ev.key === '=' || ev.key === '+' || ev.code === 'NumpadAdd') zoomIn();
		else if (ev.key === '-' || ev.key === '_' || ev.code === 'NumpadSubtract') zoomOut();
		else if (ev.key === '0' || ev.code === 'Numpad0') fit();
		else acted = false;
		if (acted) {
			// Stop here: bubbling on would reach VS Code's keybinding
			// forwarder and zoom the whole window too.
			ev.preventDefault();
			ev.stopPropagation();
		}
	}

	// Middle-button drag pans (left-drag belongs to the editors' gestures).
	let pan: { x: number; y: number; sl: number; st: number } | null = null;
	function onPointerDown(ev: PointerEvent) {
		if (ev.button !== 1 || !scroller) return;
		ev.preventDefault();
		pan = { x: ev.clientX, y: ev.clientY, sl: scroller.scrollLeft, st: scroller.scrollTop };
		scroller.setPointerCapture?.(ev.pointerId);
	}
	function onPointerMove(ev: PointerEvent) {
		if (!pan || !scroller) return;
		scroller.scrollLeft = pan.sl - (ev.clientX - pan.x);
		scroller.scrollTop = pan.st - (ev.clientY - pan.y);
	}
	function onPointerUp() {
		pan = null;
	}

	onMount(() => {
		const el = scroller!;
		// Deferred a frame: resizing the palette inside the observer's own
		// callback trips Chrome's "ResizeObserver loop" error.
		const ro = new ResizeObserver(() => requestAnimationFrame(() => (paneW = el.clientWidth)));
		ro.observe(el);
		paneW = el.clientWidth;
		el.addEventListener('wheel', onWheel, { passive: false });
		// Autoscroll's middle-click cursor would fight the pan.
		const noAuto = (ev: MouseEvent) => ev.button === 1 && ev.preventDefault();
		el.addEventListener('mousedown', noAuto);
		if (autoFit) {
			// Two frames: the view measures its pane (ResizeObserver) first.
			// The first-load fit stops at a legible 50% — a long chart then
			// scrolls; Ctrl+0 / the fit button go all the way.
			requestAnimationFrame(() => requestAnimationFrame(() => fit(AUTO_FIT_FLOOR)));
		}
		return () => {
			ro.disconnect();
			el.removeEventListener('wheel', onWheel);
			el.removeEventListener('mousedown', noAuto);
		};
	});
</script>

<div class="zpane">
	<!-- svelte-ignore a11y_no_static_element_interactions a11y_no_noninteractive_tabindex -->
	<div
		class="flow ldscroll"
		class:stale
		bind:this={scroller}
		style:--pane-w={paneW ? paneW + 'px' : undefined}
		tabindex="-1"
		onkeydown={onKey}
		onpointerdown={onPointerDown}
		onpointermove={onPointerMove}
		onpointerup={onPointerUp}
		onpointercancel={onPointerUp}
	>
		{@render children()}
		<div class="zclear" style:height="{CTL_CLEAR}px" aria-hidden="true"></div>
	</div>
	<div class="zctl" role="toolbar" aria-label="{label} zoom">
		<button title="Zoom in (Ctrl+= / Ctrl+wheel)" aria-label="zoom in" onclick={zoomIn} disabled={zoom >= MAX_ZOOM}>
			<svg viewBox="0 0 32 32"><path d="M32 18.133H18.133V32h-4.266V18.133H0v-4.266h13.867V0h4.266v13.867H32z" /></svg>
		</button>
		<button title="Zoom out (Ctrl+- / Ctrl+wheel)" aria-label="zoom out" onclick={zoomOut} disabled={zoom <= MIN_ZOOM}>
			<svg viewBox="0 0 32 5"><path d="M0 0h32v4.2H0z" /></svg>
		</button>
		<button
			title={fitAxis === 'width' ? 'Fit the widest rung to the pane (Ctrl+0)' : 'Fit the chart to the pane (Ctrl+0)'}
			aria-label="fit view"
			onclick={() => fit()}
		>
			<svg viewBox="0 0 32 30"><path d="M3.692 4.63c0-.53.4-.938.939-.938h5.215V0H4.708C2.13 0 0 2.054 0 4.63v5.216h3.692V4.631zM27.354 0h-5.2v3.692h5.17c.53 0 .984.4.984.939v5.215H32V4.631A4.624 4.624 0 0027.354 0zm.954 24.83c0 .532-.4.94-.939.94h-5.215v3.768h5.215c2.577 0 4.631-2.13 4.631-4.707v-5.139h-3.692v5.139zm-23.677.94c-.531 0-.939-.4-.939-.94v-5.138H0v5.139c0 2.577 2.13 4.707 4.708 4.707h5.138V25.77H4.631z" /></svg>
		</button>
		<span class="zpct" title="zoom level (saved with this panel, not the file)">{Math.round(zoom * 100)}%</span>
	</div>
</div>

<style>
	.zpane {
		position: relative;
		flex: 1;
		min-height: 0;
		display: flex;
	}
	.flow {
		flex: 1;
		min-width: 0;
		overflow: auto;
		outline: none;
	}
	.zclear {
		width: 1px;
		pointer-events: none;
	}
	.flow.stale {
		opacity: 0.45;
	}
	/* Same spot and look as FBD's xyflow <Controls /> (bottom-left, 26px
	   buttons), themed from the same tokens App gives those. */
	.zctl {
		position: absolute;
		left: 15px;
		bottom: 15px;
		z-index: 12;
		display: flex;
		flex-direction: column;
		align-items: stretch;
		box-shadow: var(--nx-shadow);
		border: 1px solid var(--nx-border);
		background: var(--nx-panel-bg);
	}
	.zctl button {
		display: flex;
		justify-content: center;
		align-items: center;
		width: 26px;
		height: 26px;
		padding: 4px;
		border: none;
		border-bottom: 1px solid var(--nx-border);
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		cursor: pointer;
	}
	.zctl button:hover {
		background: var(--nx-ctl-hover);
	}
	.zctl button:focus-visible {
		outline: 1px solid var(--nx-accent);
		outline-offset: -1px;
	}
	.zctl button:disabled {
		cursor: default;
		opacity: 0.4;
	}
	.zctl svg {
		width: 100%;
		max-width: 12px;
		max-height: 12px;
		fill: currentColor;
	}
	.zpct {
		font-size: 9px;
		text-align: center;
		padding: 2px 0;
		color: var(--nx-muted);
		font-variant-numeric: tabular-nums;
	}
</style>
