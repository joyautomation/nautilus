<script lang="ts">
	// The variables panel: every header declaration, referenced or not — the
	// diagram itself only draws what the logic wires up, so this is where a
	// freshly declared (still unused) variable is visible. Live values ride
	// the same store as the node pills.
	import type { VarDecl } from './layout';
	import { live, liveValue, formatLive } from './liveState.svelte';

	let {
		open = $bindable(false),
		vars,
		used
	}: { open?: boolean; vars: VarDecl[]; used: Set<string> } = $props();

	const SECTION_BADGE: Record<string, string> = {
		VAR_EXTERNAL: 'ext',
		VAR: 'local',
		VAR_INPUT: 'in',
		VAR_OUTPUT: 'out',
		VAR_IN_OUT: 'in/out'
	};
</script>

{#if open}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="panel" onclick={(e) => e.stopPropagation()}>
		<div class="head">
			<span>variables</span>
			<span class="count">{vars.length}</span>
		</div>
		{#if vars.length === 0}
			<div class="empty">no declarations — "+ add" can create one</div>
		{/if}
		{#each vars as v (v.section + ':' + v.name)}
			{@const val = liveValue(v.name)}
			<div class="row" title="line {v.line}{used.has(v.name.toLowerCase()) ? '' : ' — declared but not referenced by the logic; it appears in the diagram once something reads or writes it'}">
				<span class="badge {v.section === 'VAR_EXTERNAL' ? 'ext' : ''}">{SECTION_BADGE[v.section] ?? v.section}</span>
				<span class="name">{v.name}</span>
				<span class="type">: {v.type}{v.init ? ` := ${v.init}` : ''}</span>
				<span class="spacer"></span>
				{#if val !== undefined}
					<span class="val" class:off={!live.fresh}>{formatLive(val)}</span>
				{/if}
				{#if !used.has(v.name.toLowerCase())}
					<span class="unused">unused</span>
				{/if}
			</div>
		{/each}
	</div>
{/if}

<style>
	.panel {
		position: absolute;
		right: 8px;
		top: 30px;
		z-index: 20;
		display: flex;
		flex-direction: column;
		min-width: 280px;
		max-width: 420px;
		max-height: 70vh;
		overflow-y: auto;
		background: var(--vscode-editorWidget-background, #252526);
		border: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.4));
		border-radius: 5px;
		padding: 4px;
		box-shadow: 0 4px 14px rgba(0, 0, 0, 0.35);
		font-size: 12px;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 4px 8px 6px;
		font-weight: 600;
		color: var(--vscode-foreground, #ccc);
		border-bottom: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.3));
		margin-bottom: 3px;
	}
	.head .count {
		font-weight: 400;
		font-size: 11px;
		color: var(--vscode-descriptionForeground, #888);
	}
	.empty {
		padding: 8px;
		color: var(--vscode-descriptionForeground, #888);
		font-size: 11px;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 7px;
		padding: 3px 8px;
		border-radius: 3px;
		font-family: var(--vscode-editor-font-family, monospace);
		cursor: default;
	}
	.row:hover {
		background: var(--vscode-list-hoverBackground, rgba(128, 128, 128, 0.15));
	}
	.badge {
		font-size: 9px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 3px;
		color: var(--vscode-descriptionForeground, #999);
		border: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.4));
		min-width: 30px;
		text-align: center;
	}
	.badge.ext {
		color: var(--vscode-charts-blue, #58a6ff);
		border-color: color-mix(in srgb, var(--vscode-charts-blue, #58a6ff) 55%, transparent);
	}
	.name {
		color: var(--vscode-foreground, #ccc);
	}
	.type {
		color: var(--vscode-descriptionForeground, #888);
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
		font-weight: 600;
		padding: 1px 5px;
		border-radius: 5px;
		white-space: nowrap;
		color: var(--vscode-charts-green, #64d88a);
		background: color-mix(in srgb, var(--vscode-charts-green, #64d88a) 13%, var(--vscode-editor-background, #1e1e1e));
		border: 1px solid rgba(100, 216, 138, 0.38);
	}
	.val.off {
		color: var(--vscode-descriptionForeground, #8c8c8c);
		background: color-mix(in srgb, #8c8c8c 12%, var(--vscode-editor-background, #1e1e1e));
		border-color: rgba(140, 140, 140, 0.32);
	}
	.unused {
		font-size: 9px;
		font-style: italic;
		color: var(--vscode-editorWarning-foreground, #d7a021);
	}
</style>
