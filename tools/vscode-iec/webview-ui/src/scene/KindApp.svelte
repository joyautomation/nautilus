<script lang="ts">
	// The kind editor shell (kindEditor.ts opens it): toolbar (look,
	// turntable), the stage with the one kind on it, and the panel of its
	// fields and drives. The file arrives from the extension host (kindDoc
	// / kindError); every change goes back as an op.
	import { LOOKS, type SceneLook } from '@joyautomation/nautilus-hmi-3d';
	import { announceReady, kd, reopenAsText } from './kindState.svelte';
	import KindStage from './KindStage.svelte';
	import KindPanel from './KindPanel.svelte';

	$effect(() => {
		const onMsg = (e: MessageEvent) => {
			const m = e.data as {
				type?: string;
				kind?: typeof kd.kind;
				rendered?: typeof kd.rendered;
				model?: typeof kd.model;
				name?: string;
				title?: string;
				message?: string;
			};
			if (m?.type === 'kindDoc' && m.kind) {
				kd.kind = m.kind;
				kd.rendered = m.rendered ?? m.kind;
				kd.model = m.model ?? null;
				kd.name = m.name ?? kd.name;
				kd.title = m.title ?? kd.title;
				kd.error = '';
			} else if (m?.type === 'kindError') {
				kd.error = m.message ?? 'invalid kind file';
			}
		};
		window.addEventListener('message', onMsg);
		// Harness hook: a preloaded model outside VS Code.
		if (window.__MODEL__) onMsg(new MessageEvent('message', { data: window.__MODEL__ }));
		announceReady();
		return () => window.removeEventListener('message', onMsg);
	});

	const LOOK_TIP: Record<SceneLook, string> = {
		lit: 'Lit: the model in its own materials under the lights',
		flat: 'Flat: the model matte under plain lights',
		paper: 'Paper: the kind as a line drawing — white fills, inked edges'
	};

	function inField(e: KeyboardEvent): boolean {
		const t = e.target as HTMLElement | null;
		return !!t && (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.tagName === 'TEXTAREA' || t.isContentEditable);
	}
	function onKey(e: KeyboardEvent) {
		if (e.key === 'Escape' && inField(e)) (e.target as HTMLElement).blur();
		else if (!inField(e) && (e.key === 't' || e.key === 'T')) kd.turntable = !kd.turntable;
	}

	const hint = $derived(
		kd.modelState === 'failed'
			? 'the model did not load — check the path in the panel'
			: kd.modelState === 'ok'
				? `${kd.meshes.length} named objects in the model · pick one per drive · sample values drive the preview · T toggles the turntable`
				: 'a kind is a model and a few drives · name a .glb in the panel to see it here'
	);
</script>

<svelte:window onkeydown={onKey} />

<div class="editor">
	<header>
		<span class="title">{kd.title}</span>
		<span class="name">· kind <code>{kd.name}</code>{#if kd.kind?.type} · binds a {kd.kind.type}{/if}</span>
		<span class="tools" role="toolbar" aria-label="Look">
			{#each LOOKS as l (l)}
				<button class:active={kd.look === l} onclick={() => (kd.look = l)} title={LOOK_TIP[l]}>{l[0].toUpperCase() + l.slice(1)}</button>
			{/each}
		</span>
		<button class="toggle" class:active={kd.turntable} onclick={() => (kd.turntable = !kd.turntable)} title="Turn the kind slowly on the stage (T)">
			Turntable: {kd.turntable ? 'On' : 'Off'}
		</button>
		<span class="spacer"></span>
		<span class="state" class:bad={kd.modelState === 'failed'} class:warn={kd.model && !kd.model.file}>
			{#if !kd.kind?.model}no model{:else if kd.model && !kd.model.file}model not found{:else if kd.modelState === 'failed'}model failed to load{:else if kd.modelState === 'ok'}● model{:else}loading model…{/if}
		</span>
	</header>

	{#if kd.error && !kd.kind}
		<div class="fatal" role="alert">
			<p>This file isn't a kind the editor can open:</p>
			<pre>{kd.error}</pre>
			<button class="reopen" onclick={reopenAsText}>Reopen as Text Editor</button>
		</div>
	{:else}
		{#if kd.error}
			<div class="err" role="alert">
				<span>JSON: {kd.error} — the kind is read-only until the text parses again</span>
				<button class="reopen" onclick={reopenAsText}>Reopen as Text Editor</button>
			</div>
		{/if}
		<div class="cols" class:locked={!!kd.error} inert={!!kd.error}>
			<KindStage />
			<KindPanel />
		</div>
	{/if}

	<footer title={hint}>{hint}</footer>
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
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.name code {
		font-family: var(--nx-mono);
	}
	.tools {
		display: inline-flex;
		gap: 2px;
		border: 1px solid var(--nx-border);
		border-radius: 6px;
		padding: 2px;
	}
	.tools button,
	.toggle {
		padding: 3px 10px;
		border: 1px solid transparent;
		border-radius: 4px;
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
		font-size: 12px;
	}
	.toggle {
		border-color: var(--nx-border);
	}
	.tools button:hover,
	.toggle:hover {
		background: var(--nx-hover);
	}
	.tools button.active,
	.toggle.active {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
	}
	.spacer {
		flex: 1;
	}
	.state {
		font-size: 12px;
		color: var(--nx-muted);
	}
	.state.warn {
		color: var(--nx-warn);
	}
	.state.bad {
		color: var(--nx-err);
	}
	.err {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		font-size: 12px;
		border-bottom: 1px solid var(--nx-border);
		background: var(--nx-err-bg);
		color: var(--nx-err);
	}
	.err span {
		flex: 1;
	}
	.reopen {
		padding: 3px 10px;
		border: 1px solid var(--nx-border);
		border-radius: 4px;
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
		font-size: 12px;
	}
	.fatal {
		padding: 20px;
		color: var(--nx-ui-ink);
	}
	.fatal pre {
		font-family: var(--nx-mono);
		color: var(--nx-err);
		white-space: pre-wrap;
	}
	.cols {
		flex: 1;
		min-height: 0;
		display: flex;
	}
	.cols.locked {
		opacity: 0.6;
	}
	footer {
		flex: none;
		padding: 4px 10px;
		font-size: 11px;
		color: var(--nx-muted);
		border-top: 1px solid var(--nx-border);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
