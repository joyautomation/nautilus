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
	import { vscode } from '../vscodeApi';
	import UserIsland from './UserIsland.svelte';
	import { hasUserComponent, setUserComponentDiagnostics } from './userComponentsState.svelte';
	import { formatPropValue, isPropName, kindOf, parsePropInput } from './previewProps';

	type Diagnostic = { component: string; message: string };

	let component = $state('');
	let file = $state('');
	let sidecar = $state('');
	let hasSidecar = $state(false);
	let props = $state<Record<string, unknown>>({});
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

	const available = $derived(component !== '' && hasUserComponent(component));
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
				props?: Record<string, unknown>;
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
				props = m.props ?? {};
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
	const statusText = $derived(
		building ? 'compiling…' : error ? 'error' : available ? 'live' : component ? 'waiting for the first compile…' : ''
	);
</script>

<div class="pane">
	<header>
		<button class="link title" title="Go to the source" onclick={() => vscode.postMessage({ type: 'openSource' })}>
			{file || 'Component Preview'}
		</button>
		{#if box}<span class="size">{box.w} × {box.h}</span>{/if}
		<span class="status" class:s-building={building} class:s-err={!!error && !building} class:s-ok={available && !error && !building}>
			{statusText}
		</span>
		<span class="spacer"></span>
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
		<div class="stage" class:stale={!!error}>
			{#if available}
				{#key version}
					<div class="scale" style="transform: scale({zoom})">
						<div class="box" bind:this={measureEl}>
							<UserIsland name={component} {props} />
						</div>
					</div>
				{/key}
			{:else if component}
				<p class="empty">{error ? 'nothing to show until it compiles' : 'compiling ' + file + '…'}</p>
			{:else}
				<p class="empty">open a component's .svelte file</p>
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
		display: inline-block;
		outline: 1px dashed var(--nx-border);
		outline-offset: 4px;
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
