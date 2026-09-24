<script lang="ts">
	// Standalone editor for one *.component.json sidecar: the component named
	// by the filename, rendered centered at its natural size — REAL (the kit
	// registry's actual Svelte component) when it's a built-in, otherwise a
	// proportioned placeholder outline — with its connection points overlaid
	// as draggable dots. Same drag/add/delete gestures as the mimic editor's
	// ports-edit mode (portsGestures.ts is the shared math), so learning one
	// editor's ports mode teaches both. Today this editor only shows/edits
	// `ports` — the sidecar's one implemented key — but future keys
	// (defaults, bindHints, palette, ...) grow this same editor.
	//
	// The document is the entry (`{ "ports": [...] }`, possibly with other
	// keys this editor doesn't touch); every gesture posts the full ports
	// list and the host patches just that key, rewriting the whole file as
	// one WorkspaceEdit, so text undo/redo covers the session exactly like
	// the mimic editor.
	import { resolvedPortDir, type PortDir as HmiPortDir } from '@joyautomation/nautilus-hmi/mimic';
	import { registry, DEMO_PROPS } from './registry';
	import { cs, postComponentPortsOp, announceComponentReady, reopenAsText, type Port } from './componentState.svelte';
	import {
		fmtFraction,
		newPortAtFreeSlot,
		newPortAtPoint,
		roundPort,
		toFraction,
		type Box,
		type PortDir
	} from './portsGestures';
	import UserIsland from './UserIsland.svelte';
	import { hasUserComponent, setUserComponentDiagnostics } from './userComponentsState.svelte';
	import PortsPanel from './PortsPanel.svelte';

	$effect(() => {
		const onMsg = (e: MessageEvent) => {
			const m = e.data as {
				type?: string;
				component?: string;
				ports?: Port[];
				title?: string;
				message?: string;
				diagnostics?: { component: string; message: string }[];
			};
			if (m?.type === 'componentDoc') {
				cs.doc = { component: m.component ?? '', ports: m.ports ?? [] };
				cs.title = m.title ?? cs.title;
				cs.error = '';
			} else if (m?.type === 'componentError') {
				cs.error = m.message ?? 'invalid document';
				// Any gesture in flight is against the stale doc — drop it.
				drag = null;
			} else if (m?.type === 'userComponentDiagnostics') {
				setUserComponentDiagnostics(m.diagnostics ?? []);
			}
		};
		window.addEventListener('message', onMsg);
		// Harness hook: a preloaded model outside VS Code (see vscodeApi.ts).
		if (window.__MODEL__) onMsg(new MessageEvent('message', { data: window.__MODEL__ }));
		announceComponentReady();
		return () => window.removeEventListener('message', onMsg);
	});

	// ── stage: the component centered in a fixed working canvas ─────────────
	const STAGE_W = 480;
	const STAGE_H = 320;
	/** A custom/unknown component has no intrinsic size to measure — this is
	 * the "proportioned placeholder" working size instead. */
	const PLACEHOLDER_W = 160;
	const PLACEHOLDER_H = 100;

	let stageEl = $state<HTMLDivElement | null>(null);
	let measureEl = $state<HTMLDivElement | null>(null);
	let box = $state<Box>({ x: (STAGE_W - PLACEHOLDER_W) / 2, y: (STAGE_H - PLACEHOLDER_H) / 2, w: PLACEHOLDER_W, h: PLACEHOLDER_H });

	const isBuiltin = $derived(!!cs.doc && !!registry[cs.doc.component]);
	const isUserIsland = $derived(!isBuiltin && !!cs.doc && hasUserComponent(cs.doc.component));
	/** Real content (built-in OR a landed user island) defines the box —
	 * the dashed placeholder is only the fallback for neither. Measuring
	 * happens AFTER the real thing mounts, never before: forcing an explicit
	 * width/height on `.box` ahead of that (the old always-on style) fed the
	 * ResizeObserver its own fixed size back and could never adopt a real
	 * component's actual aspect ratio (the HeatExchanger bug this fixes). */
	const isReal = $derived(isBuiltin || isUserIsland);

	$effect(() => {
		if (!isReal || !measureEl) return;
		const el = measureEl;
		const update = () => {
			const w = el.offsetWidth || PLACEHOLDER_W;
			const h = el.offsetHeight || PLACEHOLDER_H;
			box = { x: (STAGE_W - w) / 2, y: (STAGE_H - h) / 2, w, h };
		};
		update();
		const ro = new ResizeObserver(update);
		ro.observe(el);
		return () => ro.disconnect();
	});
	$effect(() => {
		if (isReal) return;
		box = { x: (STAGE_W - PLACEHOLDER_W) / 2, y: (STAGE_H - PLACEHOLDER_H) / 2, w: PLACEHOLDER_W, h: PLACEHOLDER_H };
	});

	function pt(e: { clientX: number; clientY: number }): [number, number] {
		const r = stageEl!.getBoundingClientRect();
		return [e.clientX - r.left, e.clientY - r.top];
	}

	// ── ports + in-flight drag ───────────────────────────────────────────────
	const ports = $derived(cs.doc?.ports ?? []);
	let drag = $state<{ index: number; fx: number; fy: number; moved: boolean } | null>(null);
	let selected = $state<number | null>(null);

	/** The resolved set with the in-flight drag (if any) substituted in. */
	const displayPorts = $derived(
		ports.map((p, i) => (drag && drag.index === i ? { ...p, x: drag.fx, y: drag.fy } : p))
	);
	/** Direction tick offsets (unit vectors) — a small line off each dot
	 * showing its effective (explicit or inferred) exit direction. */
	const TICK: Record<HmiPortDir, [number, number]> = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] };
	const TICK_LEN = 10;
	const portsPx = $derived(
		displayPorts.map((p) => {
			const dir = resolvedPortDir(p);
			const x = box.x + p.x * box.w;
			const y = box.y + p.y * box.h;
			const [tx, ty] = dir ? TICK[dir] : [0, 0];
			return { name: p.name, x, y, fx: p.x, fy: p.y, dir, tickX: x + tx * TICK_LEN, tickY: y + ty * TICK_LEN };
		})
	);

	function commit(next: Port[]) {
		postComponentPortsOp(next.map(roundPort));
	}

	function portDown(e: PointerEvent, i: number) {
		if (e.button !== 0) return;
		e.stopPropagation();
		(e.currentTarget as Element).setPointerCapture(e.pointerId);
		selected = i;
		const p = ports[i];
		drag = { index: i, fx: p.x, fy: p.y, moved: false };
	}

	function portDelete(i: number) {
		commit(ports.filter((_, j) => j !== i));
		if (selected === i) selected = null;
	}

	/** Double-click on the component/placeholder outline adds a port,
	 * projected onto the nearer edge and quarter-snapped, auto-named — same
	 * gesture as the mimic editor's ports mode (see portsGestures.ts's
	 * newPortAtPoint). */
	function addPort(e: MouseEvent) {
		e.stopPropagation();
		commit([...ports, newPortAtPoint(box, pt(e), ports)]);
	}

	/** "Add port" panel button: places at the first free quarter-position
	 * slot on the outline instead of a click point (see
	 * portsGestures.ts's newPortAtFreeSlot). */
	function addPortAtFreeSlot() {
		commit([...ports, newPortAtFreeSlot(ports)]);
	}

	/** Rename via the ports panel — commits through the same op path
	 * (whole-doc edit, undoable). Empty or duplicate names are refused; the
	 * input reverts to the current name (see PortsPanel's onRename call). */
	function renamePort(i: number, name: string): boolean {
		const trimmed = name.trim();
		if (trimmed === '' || ports.some((p, j) => j !== i && p.name === trimmed)) return false;
		commit(ports.map((p, j) => (j === i ? { ...p, name: trimmed } : p)));
		return true;
	}

	/** Set (or clear, `dir: null`) a port's explicit exit direction. */
	function setPortDir(i: number, dir: PortDir | null): void {
		commit(ports.map((p, j) => (j === i ? { ...p, dir: dir ?? undefined } : p)));
	}

	function onmove(e: PointerEvent) {
		if (!drag) return;
		const [fx, fy] = toFraction(box, pt(e));
		if (fx !== drag.fx || fy !== drag.fy) {
			drag.fx = fx;
			drag.fy = fy;
			drag.moved = true;
		}
	}
	function onup() {
		if (!drag) return;
		if (drag.moved) {
			const d = drag;
			commit(ports.map((p, i) => (i === d.index ? { name: p.name, x: d.fx, y: d.fy } : p)));
		}
		drag = null;
	}

	function onkeydown(e: KeyboardEvent) {
		if (cs.error) return;
		if (e.key === 'Escape') selected = null;
	}

	const hint = 'drag a dot to move it · double-click a dot to remove · double-click the outline to add · Esc deselects';
</script>

<svelte:window onpointermove={onmove} onpointerup={onup} {onkeydown} />

<div class="editor">
	<header>
		<span class="title">{cs.title}</span>
		{#if cs.doc}<span class="name">· {cs.doc.component}</span>{/if}
	</header>

	{#if cs.error && !cs.doc}
		<!-- Never parsed: no canvas to show, only the way out. -->
		<div class="fatal" role="alert">
			<p>This file isn't a component sidecar the editor can open:</p>
			<pre>{cs.error}</pre>
			<button class="reopen" onclick={reopenAsText}>Reopen as Text Editor</button>
		</div>
	{:else}
	{#if cs.error}
		<div class="err" role="alert">
			<span>JSON: {cs.error} — the canvas is read-only until the text parses again</span>
			<button class="reopen" onclick={reopenAsText}>Reopen as Text Editor</button>
		</div>
	{/if}

	<!-- A parse error mid-edit leaves the last good doc on screen, locked:
	     a port drag against it would overwrite the half-typed text. -->
	<div class="body" class:locked={!!cs.error} inert={!!cs.error}>
		<div class="stage">
			{#if cs.doc}
				{@const C = registry[cs.doc.component]}
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div
					class="canvas"
					bind:this={stageEl}
					style="width: {STAGE_W}px; height: {STAGE_H}px"
					onpointerdown={() => (selected = null)}
				>
					<!-- svelte-ignore a11y_no_static_element_interactions -->
					<div
						class="box"
						bind:this={measureEl}
						style="left: {box.x}px; top: {box.y}px; {isReal ? '' : `width: ${box.w}px; height: ${box.h}px`}"
						ondblclick={addPort}
					>
						{#if C}
							<C {...(DEMO_PROPS[cs.doc.component] ?? {})} width={box.w} />
						{:else if isUserIsland}
							<UserIsland name={cs.doc.component} props={{ ...(DEMO_PROPS[cs.doc.component] ?? {}), width: box.w }} />
						{:else}
							<div class="placeholder">{cs.doc.component}</div>
						{/if}
					</div>

					<svg class="overlay" width={STAGE_W} height={STAGE_H} viewBox="0 0 {STAGE_W} {STAGE_H}">
						{#each portsPx as port, i (i)}
							{#if port.dir}
								<line class="tick" x1={port.x} y1={port.y} x2={port.tickX} y2={port.tickY} />
							{/if}
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<circle
								class="port"
								class:sel={selected === i}
								cx={port.x}
								cy={port.y}
								r="6"
								onpointerdown={(e) => portDown(e, i)}
								ondblclick={(e) => {
									e.stopPropagation();
									portDelete(i);
								}}
								><title
									>{port.name} · {fmtFraction(port.fx, port.fy)}{port.dir ? ` · exits ${port.dir}` : ''}</title
								></circle
							>
						{/each}
					</svg>

					{#if drag}
						<span class="coord" style="left: {box.x + drag.fx * box.w + 10}px; top: {box.y + drag.fy * box.h - 20}px">
							{fmtFraction(drag.fx, drag.fy)}
						</span>
					{/if}
				</div>
			{:else}
				<p class="empty">waiting for document…</p>
			{/if}
		</div>

		{#if cs.doc}
			<PortsPanel
				{ports}
				{selected}
				onSelect={(i) => (selected = i)}
				onRename={renamePort}
				onDelete={portDelete}
				onAdd={addPortAtFreeSlot}
				onSetDir={setPortDir}
			/>
		{/if}
	</div>

	{/if}

	<footer>{hint}</footer>
</div>

<style>
	.editor {
		height: 100%;
		display: flex;
		flex-direction: column;
	}
	header {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		border-bottom: 1px solid var(--nx-border);
		flex: none;
	}
	.title {
		font-weight: 600;
		color: var(--nx-ink);
	}
	.name {
		color: var(--nx-muted);
	}
	.err {
		flex: none;
		padding: 4px 10px;
		font-family: var(--nx-mono);
		font-size: 12px;
		color: var(--nx-err);
		background: var(--nx-err-bg);
		border-bottom: 1px solid var(--nx-border);
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.err span {
		flex: 1;
		min-width: 0;
	}
	.reopen {
		flex: none;
		font: inherit;
		font-size: 12px;
		padding: 3px 10px;
		border: none;
		border-radius: 4px;
		background: var(--nx-btn-bg);
		color: var(--nx-btn-ink);
		cursor: pointer;
	}
	.fatal {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 10px;
		padding: 24px;
		color: var(--nx-ui-ink);
	}
	.fatal p {
		margin: 0;
	}
	.fatal pre {
		margin: 0;
		max-width: 100%;
		white-space: pre-wrap;
		font-family: var(--nx-mono);
		font-size: 12px;
		color: var(--nx-err);
		background: var(--nx-err-bg);
		padding: 6px 10px;
		border-radius: 4px;
	}
	.body {
		flex: 1;
		min-height: 0;
		display: flex;
	}
	.body.locked {
		opacity: 0.5;
		filter: grayscale(0.6);
		pointer-events: none;
	}
	.stage {
		flex: 1;
		min-width: 0;
		min-height: 0;
		overflow: auto;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 24px;
	}
	.canvas {
		position: relative;
		flex: none;
	}
	.box {
		position: absolute;
		cursor: copy;
	}
	/* A bare <svg> root (every kit component, and most user components) is
	   an inline-level replaced element by default: sitting on its
	   container's text baseline reserves a few px of descender space BELOW
	   it, so `.box`'s measured offsetHeight (what the ports fractions are
	   computed against) came out taller than the svg's own visual box —
	   bottom-edge ports floated below the actual shape. block eliminates
	   the line box entirely, so measured == rendered exactly. */
	.box :global(svg) {
		display: block;
	}
	.placeholder {
		/* content-box (the default) would add this padding + border ON TOP
		   of the 100%-of-.box width/height, so the rendered box overflowed
		   `.box` outward on the right and bottom only (block layout anchors
		   the top-left corner flush) — the dots, computed from `.box`'s
		   pre-overflow size, then sat inset from the dashed outline on
		   exactly those two edges. border-box folds the padding/border
		   back inside 100%, so the placeholder always matches `.box`
		   exactly and the dots straddle it on all four sides. */
		box-sizing: border-box;
		display: flex;
		align-items: center;
		justify-content: center;
		width: 100%;
		height: 100%;
		border: 1px dashed var(--nx-err);
		border-radius: 6px;
		color: var(--nx-err);
		font-size: 12px;
		font-family: var(--nx-mono);
		text-align: center;
		padding: 4px;
	}
	.overlay {
		position: absolute;
		inset: 0;
		pointer-events: none;
	}
	.port {
		fill: var(--nx-sel-bg);
		stroke: var(--nx-sel-ink);
		stroke-width: 2px;
		pointer-events: all;
		cursor: grab;
	}
	.tick {
		stroke: var(--nx-sel-ink);
		stroke-width: 2px;
		pointer-events: none;
	}
	.port.sel {
		fill: var(--nx-sel-ink);
	}
	.coord {
		position: absolute;
		font-size: 11px;
		font-family: var(--nx-mono);
		color: var(--nx-ink);
		background: var(--nx-bg);
		border: 1px solid var(--nx-border);
		border-radius: 4px;
		padding: 1px 5px;
		pointer-events: none;
		white-space: nowrap;
	}
	.empty {
		color: var(--nx-muted);
		padding: 24px;
	}
	footer {
		flex: none;
		padding: 4px 10px;
		border-top: 1px solid var(--nx-border);
		color: var(--nx-muted);
		font-size: 11px;
	}
</style>
