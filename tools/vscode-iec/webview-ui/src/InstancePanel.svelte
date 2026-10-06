<script lang="ts">
	// The FB instance inspector: double-click a function block's body and see
	// THAT instance's live data — inputs, outputs, and internal state — the
	// PLC-IDE "open instance" view. Values ride the same live store as the
	// pills, so rows tick at frame rate.
	import Popover from './Popover.svelte';
	import { live, liveValue, liveEnum, liveTypeNote, formatLive } from './liveState.svelte';
	import { vscode } from './vscodeApi';

	let {
		inst,
		onclose
	}: {
		inst: { name: string; type: string; ins: string[]; outs: string[] };
		onclose: () => void;
	} = $props();

	// The streamed instance struct: every non-internal member by name.
	const data = $derived.by(() => {
		const v = liveValue(inst.name);
		if (v === null || typeof v !== 'object' || Array.isArray(v)) return undefined;
		return v as Record<string, unknown>;
	});
	// Rows in PLC order: inputs, outputs, then internal state alphabetically.
	const rows = $derived.by(() => {
		const d = data ?? {};
		const seen = new Set<string>();
		const row = (name: string, kind: string) => {
			seen.add(name.toLowerCase());
			return { name, kind, value: d[name] ?? d[Object.keys(d).find((k) => k.toLowerCase() === name.toLowerCase()) ?? ''] };
		};
		const out = [
			...inst.ins.map((p) => row(p, 'in')),
			...inst.outs.map((p) => row(p, 'out'))
		];
		for (const k of Object.keys(d).sort()) {
			if (!seen.has(k.toLowerCase())) out.push(row(k, 'var'));
		}
		return out;
	});
</script>

<Popover style="min-width: 260px; max-width: 380px; font-size: 12px">
	<div class="head">
		<span class="inst">{inst.name}</span>
		<span class="type">: {inst.type}</span>
		<span class="spacer"></span>
		<button class="x" title="close" onclick={onclose}>×</button>
	</div>
	{#if !live.enabled}
		<div class="empty">turn on live values to see this instance's data</div>
	{:else if data === undefined}
		<div class="empty">no live data for {inst.name} yet — is the controller running?</div>
	{:else}
		{#each rows as r (r.name)}
			<div class="row">
				<span class="badge {r.kind}">{r.kind}</span>
				<span class="name">{r.name}</span>
				<span class="spacer"></span>
				<span class="nx-pill val" class:off={!live.fresh} class:enum={liveEnum(`${inst.name}.${r.name}`)} title="{inst.name}.{r.name}{liveTypeNote(`${inst.name}.${r.name}`)}">{formatLive(r.value, `${inst.name}.${r.name}`)}</span>
			</div>
		{/each}
	{/if}
	<div class="actions">
		<button onclick={() => vscode.postMessage({ type: 'openPou', pou: inst.type })}
			title="Jump to FUNCTION_BLOCK {inst.type} in the project's .st libraries">open source</button>
	</div>
</Popover>

<style>
	.head {
		display: flex;
		align-items: center;
		gap: 4px;
		padding: 4px 8px 6px;
		font-family: var(--nx-mono);
		font-weight: 600;
		color: var(--nx-ui-ink);
		border-bottom: 1px solid var(--nx-border);
		margin-bottom: 3px;
	}
	.head .type {
		font-weight: 400;
		color: var(--nx-muted);
	}
	.x {
		background: transparent;
		border: none;
		color: var(--nx-muted);
		font-size: 14px;
		cursor: pointer;
		padding: 0 3px;
	}
	.x:hover {
		color: var(--nx-ui-ink);
	}
	.empty {
		padding: 8px;
		color: var(--nx-muted);
		font-size: 11px;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 7px;
		padding: 3px 8px;
		border-radius: 3px;
		font-family: var(--nx-mono);
	}
	.row:hover {
		background: var(--nx-hover);
	}
	.badge {
		font-size: 9px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 3px;
		color: var(--nx-muted);
		border: 1px solid var(--nx-border);
		min-width: 26px;
		text-align: center;
	}
	.badge.in {
		color: var(--nx-blue);
		border-color: color-mix(in srgb, var(--nx-blue) 55%, transparent);
	}
	.badge.out {
		color: var(--nx-ok);
		border-color: color-mix(in srgb, var(--nx-ok) 55%, transparent);
	}
	.name {
		color: var(--nx-ui-ink);
	}
	.spacer {
		flex: 1;
	}
	.val {
		font-size: 10px;
		padding: 1px 5px;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		padding: 6px 8px 4px;
	}
	.actions button {
		padding: 2px 10px;
		border-radius: 3px;
		border: 1px solid var(--nx-border);
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font-size: 12px;
	}
	.actions button:hover {
		background: var(--nx-hover);
	}
</style>
