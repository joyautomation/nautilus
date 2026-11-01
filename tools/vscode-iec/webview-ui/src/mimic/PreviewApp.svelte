<script lang="ts">
	// The component preview pane (componentPreview.ts): one `{Name}.svelte`,
	// compiled from its buffer by the host as you type, rendered for real
	// on a stage with its sample props beside it. The props come from (and
	// write back to) the `preview` key of `{Name}.component.json`, so the
	// pane is a view of two files VS Code is already editing — nothing here
	// is state the host doesn't own.
	//
	// Bundle swaps: the host posts `previewBundle` with a cache-busted URL
	// after every GOOD compile; the pane appends it as a fresh <script>
	// (carrying the page's CSP nonce) and, once it has loaded and
	// announced itself (userComponentsState's ready tick), re-keys the
	// island so the new code mounts in place of the old. No page reload,
	// so the strip, the zoom and the scroll stay put between keystrokes.
	// A failed compile posts only diagnostics: the last good island stays,
	// dimmed, under the error.
	//
	// Ports: the sidecar's `ports` (the mimic editor's pipe connection
	// points) are drawn over the live render as dots and edited right here
	// — the same gestures as the mimic editor's ports mode (portsGestures.ts
	// is the shared math) — so one pane covers a component's render, its
	// sample props and its ports, all three writing the one sidecar. The
	// dots are always shown; the gestures take the pointer only in Ports
	// mode (header toggle), so outside it the component itself stays
	// clickable.
	//
	// Built-ins: a kit component (Tank, Pump, … — registry.ts) has no source
	// here to compile, so the pane mounts the registry's own component
	// instead of a user island; its sidecar is then purely a ports override,
	// and until one exists the kit's default ports are shown (host-supplied,
	// `portsInherited`) so the first drag starts from them.
	import { vscode } from '../vscodeApi';
	import UserIsland from './UserIsland.svelte';
	import { registry, DEMO_PROPS } from './registry';
	import PortsPanel from './PortsPanel.svelte';
	import { hasUserComponent, setUserComponentDiagnostics } from './userComponentsState.svelte';
	import { formatPropValue, isPropName, kindOf, parsePropInput } from './previewProps';
	import { partRect, resolvedPortDir, type PortDir as HmiPortDir } from '@joyautomation/nautilus-hmi/mimic';
	import { themeColorMode } from '../themeMode.svelte';
	import {
		dropPort,
		fmtFraction,
		movePort,
		newPortAtFreeSlot,
		newPortAtPoint,
		roundPort,
		smallestPartAt,
		type Box,
		type Port,
		type PortDir
	} from './portsGestures';

	type Diagnostic = { component: string; message: string };

	let component = $state('');
	let file = $state('');
	let sidecar = $state('');
	let hasSidecar = $state(false);
	/** A kit component, rendered from the registry rather than a bundle. */
	let builtin = $state(false);
	let props = $state<Record<string, unknown>>({});
	let ports = $state<Port[]>([]);
	/** The ports shown are the kit's defaults (no sidecar `ports` yet). */
	let portsInherited = $state(false);
	/** Generation of the bundle currently mounted; keys the island. */
	let version = $state(0);
	let building = $state(false);
	let diagnostics = $state<Diagnostic[]>([]);
	/** A bundle the browser refused to load — its own state, since the
	 * host's next diagnostics message (none: the compile was fine) must not
	 * clear it. Cleared by the next bundle that does load. */
	let loadError = $state('');
	let zoom = $state(1);
	let box = $state<{ w: number; h: number } | null>(null);

	const available = $derived(component !== '' && (builtin ? !!registry[component] : hasUserComponent(component)));
	/** What a built-in renders with: the kit's demo props under the sidecar's. */
	const builtinProps = $derived({ ...(DEMO_PROPS[component] ?? {}), ...props });
	/** The error for THIS component, or a build-wide one ("*"). */
	const error = $derived(
		loadError || (diagnostics.find((d) => d.component === component || d.component === '*')?.message ?? '')
	);

	$effect(() => {
		const onMsg = (e: MessageEvent) => {
			const m = e.data as {
				type?: string;
				component?: string;
				file?: string;
				sidecar?: string;
				hasSidecar?: boolean;
				builtin?: boolean;
				props?: Record<string, unknown>;
				ports?: Port[];
				portsInherited?: boolean;
				src?: string;
				version?: number;
				building?: boolean;
				diagnostics?: Diagnostic[];
			};
			if (m?.type === 'previewDoc') {
				component = m.component ?? '';
				file = m.file ?? '';
				sidecar = m.sidecar ?? '';
				hasSidecar = !!m.hasSidecar;
				builtin = !!m.builtin;
				props = m.props ?? {};
				ports = m.ports ?? [];
				portsInherited = !!m.portsInherited;
				drag = null;
				if (selected !== null && selected >= ports.length) selected = null;
			} else if (m?.type === 'previewBundle' && m.src && m.version) {
				loadBundle(m.src, m.version);
			} else if (m?.type === 'previewDiagnostics') {
				building = !!m.building;
				diagnostics = m.diagnostics ?? [];
				setUserComponentDiagnostics(diagnostics);
			}
		};
		window.addEventListener('message', onMsg);
		if (window.__MODEL__) onMsg(new MessageEvent('message', { data: window.__MODEL__ }));
		vscode.postMessage({ type: 'previewReady' });
		return () => window.removeEventListener('message', onMsg);
	});

	/** A line for the host's "nautilus preview" output channel. */
	function log(message: string) {
		vscode.postMessage({ type: 'previewLog', message });
	}
	if (typeof document !== 'undefined') {
		document.addEventListener('securitypolicyviolation', (e) => {
			log(`CSP blocked ${e.violatedDirective}: ${e.blockedURI}`);
		});
	}

	// Svelte 5 scopes a component's CSS under `svelte-` + a hash of the
	// FILENAME (not of the CSS), and its runtime injector skips a style
	// element whose id is already in the document — so without this, the
	// stylesheet the FIRST bundle injected would outlive every rebuild and a
	// colour change in the component's style block would never show (the
	// mimic editor never hits it: it reloads the whole page per rebuild).
	// Drop them right before the keyed remount; the new bundle's components
	// re-inject theirs on mount. The pane's own stylesheet is a link element,
	// not one of these. (No angle brackets in this comment: Svelte's parser
	// trips on a literal style tag inside a script block, even commented.)
	function dropInjectedStyles() {
		for (const el of document.head.querySelectorAll('style[id^="svelte-"]')) el.remove();
	}

	function loadBundle(src: string, v: number) {
		document.getElementById('nx-preview-bundle')?.remove();
		const s = document.createElement('script');
		s.id = 'nx-preview-bundle';
		// The page's CSP allows scripts by nonce only; a script element
		// created here inherits nothing, and the loader tag's own nonce
		// can't be read back (the browser hides it — in VS Code's webview
		// it reads as empty), so the host stamps it on <body data-nonce>.
		const nonce = document.body.dataset.nonce ?? '';
		s.nonce = nonce;
		s.src = src;
		s.onload = () => {
			const has = !!window.__NX_USER_COMPONENTS__?.[component];
			log(`bundle v${v} loaded; registry has ${component}: ${has}`);
			loadError = '';
			dropInjectedStyles();
			version = v;
		};
		s.onerror = () => {
			log(`bundle v${v} failed to load (nonce ${nonce ? 'set' : 'EMPTY'}): ${src}`);
			loadError = 'the preview bundle failed to load — see the "nautilus preview" output channel';
		};
		log(`loading bundle v${v} (nonce ${nonce ? 'set' : 'EMPTY'})`);
		document.body.appendChild(s);
	}

	// ── the stage: measured size, zoom ───────────────────────────────────────
	let measureEl = $state<HTMLDivElement | null>(null);
	$effect(() => {
		if (!measureEl || !available) {
			box = null;
			return;
		}
		const el = measureEl;
		const update = () => {
			box = { w: Math.round(el.offsetWidth), h: Math.round(el.offsetHeight) };
		};
		update();
		const ro = new ResizeObserver(update);
		ro.observe(el);
		return () => ro.disconnect();
	});
	const ZOOMS = [0.5, 1, 2, 4];

	// ── the props strip ──────────────────────────────────────────────────────
	// Every edit is the whole map, posted to the host, which patches the
	// sidecar as one WorkspaceEdit and confirms with a fresh previewDoc.
	// The map is updated here first so the island moves under the cursor
	// without waiting for the round trip.
	function commit(next: Record<string, unknown>) {
		props = next;
		vscode.postMessage({ type: 'previewSetProps', props: $state.snapshot(next) });
	}
	function setProp(name: string, text: string) {
		const value = parsePropInput(text);
		if (Object.is(props[name], value)) return;
		commit({ ...props, [name]: value });
	}
	function removeProp(name: string) {
		const next = { ...props };
		delete next[name];
		commit(next);
	}
	let newName = $state('');
	let newValue = $state('');
	const newNameOk = $derived(isPropName(newName) && !(newName in props));
	function addProp() {
		if (!newNameOk) return;
		commit({ ...props, [newName]: parsePropInput(newValue) });
		newName = '';
		newValue = '';
	}
	function onValueKey(e: KeyboardEvent & { currentTarget: HTMLInputElement }, name: string) {
		if (e.key === 'Enter') {
			setProp(name, e.currentTarget.value);
			e.currentTarget.blur();
		} else if (e.key === 'Escape') {
			e.currentTarget.value = formatPropValue(props[name]);
			e.currentTarget.blur();
		}
	}
	const propNames = $derived(Object.keys(props));

	// ── ports: dots over the render, the panel beside ────────────────────────
	// Fractions of the rendered box, exactly as the mimic editor reads them.
	// Pointer math works off the box's on-screen rect, so zoom never enters
	// into it; the dots live inside the scaled box and shrink their radius by
	// the zoom to stay the same size on screen.
	let portsMode = $state(false);
	let boxEl = $state<HTMLDivElement | null>(null);
	let drag = $state<{ index: number; fx: number; fy: number; part?: string; moved: boolean } | null>(null);
	let selected = $state<number | null>(null);
	const TICK: Record<HmiPortDir, [number, number]> = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] };
	const TICK_LEN = 10;
	const displayPorts = $derived(
		ports.map((p, i) => (drag && drag.index === i ? movePort(p, { x: drag.fx, y: drag.fy, part: drag.part }) : p))
	);
	/** The rectangle a port's fractions are of, in the render's own
	 * (unscaled) pixels: its `part` of the drawing (measured off the DOM)
	 * when it names one the render has, else the whole box. */
	function portFrame(p: Pick<Port, 'part'>): Box {
		const r = p.part && boxEl ? partRect(boxEl, p.part) : undefined;
		return r ?? { x: 0, y: 0, w: box?.w ?? 0, h: box?.h ?? 0 };
	}
	/** Every named part of the render (`data-part`), as [name, frame]. */
	function parts(): [string, Box][] {
		if (!boxEl) return [];
		const names = new Set([...boxEl.querySelectorAll<Element>('[data-part]')].map((n) => n.getAttribute('data-part')!));
		return [...names].map((name) => [name, portFrame({ part: name })]);
	}
	const portsPx = $derived(
		displayPorts.map((p) => {
			const f = portFrame(p);
			const dir = resolvedPortDir(p);
			const x = f.x + p.x * f.w;
			const y = f.y + p.y * f.h;
			const [tx, ty] = dir ? TICK[dir] : [0, 0];
			return { name: p.name, x, y, fx: p.x, fy: p.y, dir, tickX: x + (tx * TICK_LEN) / zoom, tickY: y + (ty * TICK_LEN) / zoom };
		})
	);
	/** The box in the render's own (unscaled) pixels — the space portFrame
	 * and parts() measure in — and the pointer in it, zoom divided out. */
	function portSpace(e: { clientX: number; clientY: number }): { box: Box; pt: [number, number] } {
		const el = boxEl!;
		const r = el.getBoundingClientRect();
		const k = el.offsetWidth ? r.width / el.offsetWidth : 1;
		return { box: { x: 0, y: 0, w: el.offsetWidth, h: el.offsetHeight }, pt: [(e.clientX - r.left) / k, (e.clientY - r.top) / k] };
	}
	/** Every ports edit is the whole list to the host, which patches the
	 * sidecar's `ports` key (same path as the standalone editor) and
	 * confirms with a fresh previewDoc; `ports` is updated here first so
	 * the dot lands where it was dropped without waiting on the round trip. */
	function commitPorts(next: Port[]) {
		ports = next.map(roundPort);
		vscode.postMessage({ type: 'previewSetPorts', ports: $state.snapshot(ports) });
	}
	function portDown(e: PointerEvent, i: number) {
		if (!portsMode || e.button !== 0) return;
		e.stopPropagation();
		// Cancelling pointerdown stops the browser starting a text selection
		// or a native drag of one (the double-click that adds a port leaves
		// the render's text selected); a native drag would fire pointercancel
		// a few pixels in and take the dot's pointer capture with it.
		e.preventDefault();
		window.getSelection()?.removeAllRanges();
		(e.currentTarget as Element).setPointerCapture(e.pointerId);
		selected = i;
		const p = ports[i];
		drag = { index: i, fx: p.x, fy: p.y, part: p.part, moved: false };
	}
	function portDelete(i: number) {
		commitPorts(ports.filter((_, j) => j !== i));
		if (selected === i) selected = null;
	}
	/** Double-click on the render in Ports mode: a port on the nearer edge,
	 * quarter-snapped, auto-named (portsGestures.ts's newPortAtPoint). */
	function addPortAt(e: MouseEvent) {
		if (!portsMode || !boxEl) return;
		e.stopPropagation();
		// On a part of the drawing the new port anchors to that part, as in
		// the mimic editor's ports mode.
		const { box: b, pt } = portSpace(e);
		const hit = smallestPartAt(parts(), pt);
		commitPorts([...ports, hit ? { ...newPortAtPoint(hit[1], pt, ports), part: hit[0] } : newPortAtPoint(b, pt, ports)]);
	}
	function addPortAtFreeSlot() {
		commitPorts([...ports, newPortAtFreeSlot(ports)]);
	}
	function renamePort(i: number, name: string): boolean {
		const trimmed = name.trim();
		if (trimmed === '' || ports.some((p, j) => j !== i && p.name === trimmed)) return false;
		commitPorts(ports.map((p, j) => (j === i ? { ...p, name: trimmed } : p)));
		return true;
	}
	function setPortDir(i: number, dir: PortDir | null): void {
		commitPorts(ports.map((p, j) => (j === i ? { ...p, dir: dir ?? undefined } : p)));
	}
	function onPortMove(e: PointerEvent) {
		if (!drag || !boxEl) return;
		// No button down mid-drag: the release happened where no pointerup
		// reached us (outside the webview, after Chromium dropped the
		// capture). Drop where the dot is, rather than let it follow a
		// buttonless mouse until the next click commits a stray position.
		if (!(e.buttons & 1)) {
			onPortUp();
			return;
		}
		// Over a part the port anchors to it; off every part, to the box.
		const { box: b, pt } = portSpace(e);
		const to = dropPort(b, parts(), pt);
		if (to.x !== drag.fx || to.y !== drag.fy || to.part !== drag.part) {
			drag.fx = to.x;
			drag.fy = to.y;
			drag.part = to.part;
			drag.moved = true;
		}
	}
	/** A capture lost while the button is still down is not a drop: Chromium
	 * can drop the capture at the first move after the press (the gesture
	 * harness catches it when the drag's coordinate tag mounts beside the
	 * dot), and ending the drag there froze the dot where it was pressed.
	 * The window's pointermove / pointerup carry the drag on; a native drag
	 * of a selection still ends it through pointercancel. */
	function onPortCaptureLost(e: PointerEvent) {
		if (e.buttons & 1) return;
		onPortUp();
	}
	/** The drop. Wired to the dot itself (pointerup, pointercancel, and
	 * lostpointercapture with no button down) as well as the window: under pointer capture the
	 * release is delivered to the captured element, and the capture can also
	 * end without any pointerup at all. Idempotent — whichever fires first
	 * commits, the rest find no drag. */
	function onPortUp() {
		if (!drag) return;
		if (drag.moved) {
			const d = drag;
			// A drag moves the dot (and may change its part); everything else
			// the port carries (an explicit `dir`, …) is kept (#130).
			commitPorts(ports.map((p, i) => (i === d.index ? movePort(p, { x: d.fx, y: d.fy, part: d.part }) : p)));
		}
		drag = null;
	}
	function onKey(e: KeyboardEvent) {
		if (e.key === 'Escape') selected = null;
	}
	/** Where the drag's coordinate readout goes, in VIEWPORT pixels: just
	 * up and right of the dragged dot. The readout is position: fixed at the
	 * pane's top level, outside the scaled box and the scrolling stage —
	 * inside them it counted toward the stage's scrollable overflow, so a
	 * dot dragged near the right edge of a narrow pane put up a scrollbar
	 * mid-drag, the centred stage moved the box under the pointer, and the
	 * dot landed off the cursor (#470/#471). */
	function readoutAt(): { left: number; top: number } | null {
		const p = drag ? portsPx[drag.index] : undefined;
		if (!p || !boxEl) return null;
		const r = boxEl.getBoundingClientRect();
		const k = boxEl.offsetWidth ? r.width / boxEl.offsetWidth : 1;
		return { left: r.left + p.x * k + 10, top: r.top + p.y * k - 20 };
	}
	const statusText = $derived(
		builtin ? 'built-in' : building ? 'compiling…' : error ? 'error' : available ? 'live' : component ? 'waiting for the first compile…' : ''
	);
</script>

<svelte:window onpointermove={onPortMove} onpointerup={onPortUp} onkeydown={onKey} />

<div class="pane">
	{#if drag}
		{@const at = readoutAt()}
		{#if at}
			<span class="coord" style="left: {at.left}px; top: {at.top}px">{fmtFraction(drag.fx, drag.fy)}</span>
		{/if}
	{/if}
	<header>
		{#if builtin}
			<span class="title" title="A kit component — rendered from the kit, no source here">{component}</span>
		{:else}
			<button class="link title" title="Go to the source" onclick={() => vscode.postMessage({ type: 'openSource' })}>
				{file || 'Component Preview'}
			</button>
		{/if}
		{#if box}<span class="size">{box.w} × {box.h}</span>{/if}
		<span class="status" class:s-building={building} class:s-err={!!error && !building} class:s-ok={available && !error && !building}>
			{statusText}
		</span>
		<span class="spacer"></span>
		<button
			class="mode"
			class:on={portsMode}
			data-kind="ports-toggle"
			title="Ports mode: drag a dot to move it, double-click the render to add one, double-click a dot to remove it"
			onclick={() => (portsMode = !portsMode)}
		>
			ports {portsMode ? 'on' : 'off'}
		</button>
		<label class="zoom">
			zoom
			<select bind:value={zoom}>
				{#each ZOOMS as z (z)}<option value={z}>{z * 100}%</option>{/each}
			</select>
		</label>
	</header>

	{#if error}
		<div class="banner" role="alert"><pre>{error}</pre></div>
	{/if}

	<div class="body">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div class="stage" class:stale={!!error} ondragstart={(e) => e.preventDefault()}>
			{#if available}
				{#key version}
					<div class="scale" style="transform: scale({zoom})">
						<div class="box" data-theme={themeColorMode.mode} bind:this={measureEl}>
							<div bind:this={boxEl}>
								{#if builtin}
									{@const C = registry[component]}
									<C {...builtinProps} />
								{:else}
									<UserIsland name={component} {props} />
								{/if}
							</div>
							{#if box}
								<!-- svelte-ignore a11y_no_static_element_interactions -->
								<svg
									class="ports"
									class:on={portsMode}
									width={box.w}
									height={box.h}
									viewBox="0 0 {box.w} {box.h}"
									ondblclick={addPortAt}
									onpointerdown={() => (selected = null)}
								>
									{#each portsPx as port, i (i)}
										{#if port.dir}
											<line class="tick" x1={port.x} y1={port.y} x2={port.tickX} y2={port.tickY} style="stroke-width: {2 / zoom}px" />
										{/if}
										<!-- svelte-ignore a11y_no_static_element_interactions -->
										<circle
											class="port"
											data-id={port.name}
											data-kind="port"
											class:sel={selected === i}
											cx={port.x}
											cy={port.y}
											r={6 / zoom}
											style="stroke-width: {2 / zoom}px"
											onpointerdown={(e) => portDown(e, i)}
											onpointerup={onPortUp}
											onpointercancel={onPortUp}
											onlostpointercapture={onPortCaptureLost}
											ondblclick={(e) => {
												e.stopPropagation();
												if (portsMode) portDelete(i);
											}}
											><title>{port.name} · {fmtFraction(port.fx, port.fy)}{port.dir ? ` · exits ${port.dir}` : ''}</title></circle
										>
									{/each}
								</svg>
							{/if}
						</div>
					</div>
				{/key}
			{:else if component}
				<p class="empty">{error ? 'nothing to show until it compiles' : 'compiling ' + file + '…'}</p>
			{:else}
				<p class="empty">open a component's .svelte or .component.json file</p>
			{/if}
		</div>

		<aside>
			<h4>Props <span class="dim">{propNames.length}</span></h4>
			{#if propNames.length === 0}
				<p class="note">No sample props yet — the component renders with its defaults. Add one below.</p>
			{/if}
			{#each propNames as name (name)}
				<div class="row">
					<span class="name" title={name}>{name}</span>
					<span class="kind">{kindOf(props[name])}</span>
					<input
						class="nx-input value"
						value={formatPropValue(props[name])}
						onchange={(e) => setProp(name, e.currentTarget.value)}
						onkeydown={(e) => onValueKey(e, name)}
						spellcheck="false"
					/>
					<button class="del" title="Remove {name}" onclick={() => removeProp(name)}>✕</button>
				</div>
			{/each}
			<form
				class="add"
				onsubmit={(e) => {
					e.preventDefault();
					addProp();
				}}
			>
				<input class="nx-input" placeholder="prop" bind:value={newName} spellcheck="false" />
				<input class="nx-input" placeholder="value" bind:value={newValue} spellcheck="false" />
				<button type="submit" disabled={!newNameOk}>Add</button>
			</form>
			<p class="note">
				Values read as JSON when they parse (<code>62</code>, <code>true</code>, <code>[1, 2]</code>), else as text.
			</p>
			<p class="note">
				Saved to
				<button class="link" onclick={() => vscode.postMessage({ type: 'openSidecar' })}>{sidecar}</button>
				{hasSidecar ? '' : '(created on the first add)'}
			</p>

			<div class="ports-panel">
				<PortsPanel
					{ports}
					{selected}
					onSelect={(i) => (selected = i)}
					onRename={renamePort}
					onDelete={portDelete}
					onAdd={addPortAtFreeSlot}
					onSetDir={setPortDir}
				/>
				{#if portsInherited}
					<p class="note">These are the kit's default ports for {component}; the first change writes them all to {sidecar} as this project's override.</p>
				{/if}
				<p class="note">
					{portsMode
						? 'Drag a dot · double-click the render to add · double-click a dot to remove · Esc deselects'
						: 'Turn ports on in the header to move, add or remove them on the render.'}
				</p>
			</div>
		</aside>
	</div>
</div>

<style>
	.pane {
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
		font-size: 12px;
	}
	.title {
		font-weight: 600;
		color: var(--nx-ink);
		font-size: 13px;
	}
	.link {
		font: inherit;
		padding: 0;
		border: none;
		background: none;
		color: var(--nx-link);
		cursor: pointer;
	}
	.link:hover {
		text-decoration: underline;
	}
	.size {
		font-family: var(--nx-mono);
		color: var(--nx-muted);
	}
	.status {
		color: var(--nx-muted);
	}
	.status.s-ok {
		color: var(--nx-ok);
	}
	.status.s-err {
		color: var(--nx-err);
	}
	.status.s-building {
		color: var(--nx-warn);
	}
	.spacer {
		flex: 1;
	}
	.zoom {
		display: flex;
		align-items: center;
		gap: 4px;
		color: var(--nx-muted);
	}
	.zoom select {
		font: inherit;
		font-size: 12px;
		background: var(--nx-input-bg);
		color: var(--nx-input-ink);
		border: 1px solid var(--nx-border);
		border-radius: 4px;
	}
	.banner {
		flex: none;
		max-height: 40%;
		overflow: auto;
		padding: 6px 10px;
		font-family: var(--nx-mono);
		font-size: 12px;
		color: var(--nx-err);
		background: var(--nx-err-bg);
		border-bottom: 1px solid var(--nx-border);
	}
	.banner pre {
		margin: 0;
		white-space: pre-wrap;
	}
	.body {
		flex: 1;
		min-height: 0;
		display: flex;
	}
	.stage {
		flex: 1;
		min-width: 0;
		overflow: auto;
		display: grid;
		place-items: center;
		padding: 24px;
		/* a scrollbar that comes and goes (zoomed past the pane) must not
		   re-centre the render under a dragged dot */
		scrollbar-gutter: stable both-edges;
		background-color: var(--nx-bg);
		background-image: radial-gradient(var(--nx-border) 1px, transparent 1px);
		background-size: 16px 16px;
	}
	.stage.stale {
		opacity: 0.55;
		filter: grayscale(0.5);
	}
	.scale {
		transform-origin: center;
	}
	.box {
		position: relative;
		display: inline-block;
		/* the kit's own palette (data-theme), on the HMI's card colour */
		background-color: var(--surface);
		border-radius: 6px;
		/* a dot drag must not select the render's text under it, nor start
		   a native drag of a selection (which cancels the pointer) */
		user-select: none;
		outline: 1px dashed var(--nx-border);
		outline-offset: 4px;
	}
	/* A bare svg root is inline-level and reserves
	   descender space below itself, so the measured box would be taller
	   than the shape and bottom-edge dots would float. */
	.box :global(svg) {
		display: block;
	}
	.mode {
		font: inherit;
		font-size: 12px;
		padding: 2px 8px;
		border: 1px solid var(--nx-border);
		border-radius: 4px;
		background: transparent;
		color: var(--nx-muted);
		cursor: pointer;
	}
	.mode.on {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
		border-color: var(--nx-sel-ink);
	}
	svg.ports {
		position: absolute;
		inset: 0;
		overflow: visible;
		pointer-events: none;
	}
	/* Ports mode: the overlay takes the pointer so a double-click on the
	   render adds a port instead of reaching the component. */
	svg.ports.on {
		pointer-events: all;
		cursor: copy;
	}
	.port {
		fill: var(--nx-sel-bg);
		stroke: var(--nx-sel-ink);
		pointer-events: none;
	}
	svg.ports.on .port {
		pointer-events: all;
		cursor: grab;
	}
	.port.sel {
		fill: var(--nx-sel-ink);
	}
	.tick {
		stroke: var(--nx-sel-ink);
		pointer-events: none;
	}
	.coord {
		position: fixed;
		z-index: 10;
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
	.ports-panel {
		margin-top: 8px;
		padding-top: 8px;
		border-top: 1px solid var(--nx-border);
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	/* PortsPanel is its own side column elsewhere; here it is a section. */
	.ports-panel :global(aside) {
		width: auto;
		border-left: none;
		padding: 0;
		overflow: visible;
	}
	.empty {
		color: var(--nx-muted);
		font-size: 12px;
	}
	aside {
		flex: none;
		width: 240px;
		min-width: 0;
		box-sizing: border-box;
		overflow: hidden auto;
		border-left: 1px solid var(--nx-border);
		padding: 10px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		font-size: 12px;
	}
	/* Inputs keep an intrinsic width (size=20) that a 240px column can't
	   hold two of; let the grid tracks size them instead. */
	aside input {
		min-width: 0;
		width: 100%;
	}
	h4 {
		margin: 0 0 2px;
		font-size: 12px;
		color: var(--nx-ink);
	}
	.dim {
		color: var(--nx-muted);
		font-weight: 400;
		font-family: var(--nx-mono);
	}
	.note {
		margin: 0;
		font-size: 11px;
		color: var(--nx-muted);
	}
	.note code {
		font-family: var(--nx-mono);
	}
	.row {
		display: grid;
		min-width: 0;
		grid-template-columns: minmax(0, 1fr) auto auto;
		grid-template-rows: auto auto;
		gap: 2px 6px;
		align-items: center;
		padding: 5px 6px;
		border: 1px solid var(--nx-border);
		border-radius: 5px;
	}
	.name {
		font-family: var(--nx-mono);
		color: var(--nx-ink);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.kind {
		font-size: 10px;
		color: var(--nx-muted);
	}
	.value {
		grid-column: 1 / 3;
		grid-row: 2;
		font-family: var(--nx-mono);
	}
	.del {
		grid-column: 3;
		grid-row: 1 / 3;
		font: inherit;
		border: none;
		background: none;
		color: var(--nx-muted);
		cursor: pointer;
		padding: 0 2px;
	}
	.del:hover {
		color: var(--nx-err);
	}
	.add {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
		gap: 4px;
		margin-top: 4px;
	}
	.add button {
		font: inherit;
		font-size: 12px;
		padding: 2px 8px;
		border: none;
		border-radius: 4px;
		background: var(--nx-btn-bg);
		color: var(--nx-btn-ink);
		cursor: pointer;
	}
	.add button:disabled {
		opacity: 0.5;
		cursor: default;
	}
</style>
