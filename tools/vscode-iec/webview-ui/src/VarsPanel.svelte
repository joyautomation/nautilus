<script lang="ts">
	// The variables panel: every header declaration, referenced or not — the
	// diagram itself only draws what the logic wires up, so this is where a
	// freshly declared (still unused) variable is visible. Live values ride
	// the same store as the node pills. The footer row declares in place —
	// no round-trip through the "+ add" palette.
	import type { VarDecl } from './layout';
	import Popover from './Popover.svelte';
	import Suggest from './Suggest.svelte';
	import { TYPES } from './suggest';
	import { live, liveValue, liveMissing, formatLive } from './liveState.svelte';
	import { postOp } from './vscodeApi';

	// The panel is editor-agnostic: FBD and LD share it, differing only in
	// how a declare/delete lands (fbd edit vs ld edit ops) — so those are
	// injectable, defaulting to the FBD shapes.
	let {
		open = $bindable(false),
		vars,
		used,
		onDeclare = (name: string, type: string, section: string) =>
			postOp({ type: 'declareVar', newName: name, value: type, text: section }),
		onDelete = (name: string) => postOp({ type: 'deleteVar', newName: name }),
		readonly = false
	}: {
		open?: boolean;
		/** List only — no declare row, no delete buttons (an L5X export). */
		readonly?: boolean;
		vars: VarDecl[];
		used: Set<string>;
		onDeclare?: (name: string, type: string, section: string) => void;
		onDelete?: (name: string) => void;
	} = $props();

	const SECTION_BADGE: Record<string, string> = {
		VAR_EXTERNAL: 'ext',
		VAR: 'local',
		VAR_INPUT: 'in',
		VAR_OUTPUT: 'out',
		VAR_IN_OUT: 'in/out'
	};

	let newName = $state('');
	let newType = $state('REAL');
	let newSection = $state<'VAR_EXTERNAL' | 'VAR'>('VAR_EXTERNAL');
	const nameOk = $derived(/^[A-Za-z_][A-Za-z0-9_]*$/.test(newName.trim()));
	function addVar() {
		if (!nameOk) return;
		onDeclare(newName.trim(), newType.trim() || 'REAL', newSection);
		newName = '';
	}
</script>

{#if open}
	<Popover style="min-width: 300px; max-width: 420px; font-size: 12px">
		<div class="head">
			<span>variables</span>
			<span class="count">{vars.length}</span>
		</div>
		<!-- Only the list scrolls: the add row stays put and its type
		     dropdown can overflow the card instead of being clipped. -->
		<div class="rows">
			{#if vars.length === 0}
				<div class="empty">no declarations{readonly ? '' : ' — add one below'}</div>
			{/if}
			{#each vars as v (v.section + ':' + v.name)}
				{@const val = liveValue(v.name)}
				<div class="row" title="line {v.line}{used.has(v.name.toLowerCase()) ? '' : ' — declared but not referenced by the logic; it appears in the diagram once something reads or writes it'}">
					<span class="badge {v.section === 'VAR_EXTERNAL' ? 'ext' : ''}">{SECTION_BADGE[v.section] ?? v.section}</span>
					<span class="name">{v.name}</span>
					<span class="type">: {v.type}{v.init ? ` := ${v.init}` : ''}</span>
					<span class="spacer"></span>
					{#if val !== undefined}
						<span class="nx-pill val" class:off={!live.fresh}>{formatLive(val)}</span>
					{/if}
					{#if v.section === 'VAR_EXTERNAL' && liveMissing(v.name)}
						<span
							class="notag"
							title="No '{v.name}' tag on the controller — seed it in the runtime, drive it from a driver or the HMI, or write it from logic. A READ of a tag that was never written faults the scan."
						>no tag</span>
					{/if}
					{#if !used.has(v.name.toLowerCase())}
						<span class="unused">unused</span>
					{/if}
					{#if !readonly}
						<button
							class="del"
							title="Delete this declaration (references it still has become diagnostics)"
							onclick={() => onDelete(v.name)}
						>×</button>
					{/if}
				</div>
			{/each}
		</div>
		{#if !readonly}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="addrow"
			onkeydown={(e) => {
				e.stopPropagation();
				if (e.key === 'Enter') addVar();
			}}
		>
			<button
				class="badge toggle"
				class:ext={newSection === 'VAR_EXTERNAL'}
				title={newSection === 'VAR_EXTERNAL'
					? 'external tag (VAR_EXTERNAL) — click for a retained local (VAR)'
					: 'retained local (VAR) — click for an external tag (VAR_EXTERNAL)'}
				onclick={() => (newSection = newSection === 'VAR_EXTERNAL' ? 'VAR' : 'VAR_EXTERNAL')}
			>{newSection === 'VAR_EXTERNAL' ? 'ext' : 'local'}</button>
			<input class="nx-input grow" placeholder="name" spellcheck="false" bind:value={newName} />
			<Suggest cls="typefield" bind:value={newType} items={TYPES} />
			<button class="add" disabled={!nameOk} title="Declare (Enter)" onclick={addVar}>+</button>
		</div>
		{/if}
	</Popover>
{/if}

<style>
	.rows {
		max-height: 55vh;
		overflow-y: auto;
	}
	.addrow {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 6px 8px 4px;
		border-top: 1px solid var(--nx-border);
		margin-top: 3px;
	}
	.addrow .grow {
		flex: 1;
		min-width: 0;
	}
	.addrow :global(.typefield) {
		width: 84px;
	}
	.badge.toggle {
		background: transparent;
		cursor: pointer;
	}
	.add {
		background: transparent;
		border: 1px solid var(--nx-border);
		border-radius: 3px;
		color: var(--nx-ui-ink);
		font-size: 13px;
		line-height: 1;
		padding: 2px 7px;
		cursor: pointer;
	}
	.add:hover:enabled {
		background: var(--nx-hover);
	}
	.add:disabled {
		opacity: 0.4;
		cursor: default;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 4px 8px 6px;
		font-weight: 600;
		color: var(--nx-ui-ink);
		border-bottom: 1px solid var(--nx-border);
		margin-bottom: 3px;
	}
	.head .count {
		font-weight: 400;
		font-size: 11px;
		color: var(--nx-muted);
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
		cursor: default;
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
		min-width: 30px;
		text-align: center;
	}
	.badge.ext {
		color: var(--nx-blue);
		border-color: color-mix(in srgb, var(--nx-blue) 55%, transparent);
	}
	.name {
		color: var(--nx-ui-ink);
	}
	.type {
		color: var(--nx-muted);
		font-size: 11px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.spacer {
		flex: 1;
	}
	.val {
		font-size: 10px;
		padding: 1px 5px;
	}
	.unused {
		font-size: 9px;
		font-style: italic;
		color: var(--nx-warn);
	}
	.notag {
		font-size: 9px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 3px;
		color: var(--nx-warn);
		border: 1px solid color-mix(in srgb, var(--nx-warn) 60%, transparent);
		cursor: help;
	}
	.del {
		background: transparent;
		border: none;
		color: var(--nx-muted);
		font-size: 13px;
		line-height: 1;
		padding: 0 3px;
		cursor: pointer;
		border-radius: 3px;
		visibility: hidden;
	}
	.row:hover .del {
		visibility: visible;
	}
	.del:hover {
		color: var(--nx-err);
		background: color-mix(in srgb, var(--nx-err) 15%, transparent);
	}
</style>
