<script lang="ts">
	// The quiet half of the screen: the controller's name and a column of
	// labels and values. Beside the model in a wide window, under it in a
	// square or tall one.
	export interface Row {
		label: string;
		value: string;
		unit?: string;
		/** A lamp before the value: lit or dark. */
		lamp?: boolean;
		/** Colours the value: a fault, or a state worth a second look. */
		tone?: 'good' | 'warn' | 'crit';
	}

	let { name, model, rows, wide }: { name: string; model: string; rows: Row[]; wide: boolean } = $props();
</script>

<aside class:wide>
	<header>
		<b>{name}</b>
		<span>{model}</span>
	</header>
	<dl>
		{#each rows as r (r.label)}
			<div>
				<dt>{r.label}</dt>
				<dd class={r.tone}>
					{#if r.lamp !== undefined}<i class:lit={r.lamp}></i>{/if}{r.value}{#if r.unit}<small>{r.unit}</small>{/if}
				</dd>
			</div>
		{/each}
	</dl>
</aside>

<style>
	aside {
		box-sizing: border-box;
		padding: 1.1em 1.4em 1.3em;
		background: var(--surface-2, #232321);
		border-top: 1px solid var(--border, rgba(255, 255, 255, 0.1));
		font-size: clamp(14px, 1.75vmin, 22px);
		line-height: 1.25;
	}
	aside.wide {
		border-top: 0;
		border-left: 1px solid var(--border, rgba(255, 255, 255, 0.1));
		padding: 1.6em 1.5em;
		overflow: hidden;
	}
	header {
		display: flex;
		flex-direction: column;
		gap: 0.15em;
		margin-bottom: 0.9em;
	}
	aside:not(.wide) header {
		flex-direction: row;
		align-items: baseline;
		gap: 0.8em;
	}
	header b {
		font-size: 1.3em;
		font-weight: 650;
	}
	header span {
		color: var(--muted, #898781);
	}
	dl {
		margin: 0;
		display: grid;
		grid-template-columns: 1fr;
		column-gap: 2.4em;
	}
	aside:not(.wide) dl {
		grid-template-columns: 1fr 1fr;
	}
	dl div {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 1em;
		padding: 0.42em 0;
		border-bottom: 1px solid var(--border, rgba(255, 255, 255, 0.1));
		min-width: 0;
	}
	dt {
		color: var(--ink-2, #c3c2b7);
		white-space: nowrap;
	}
	dd {
		margin: 0;
		font-weight: 650;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	dd small {
		font-size: 0.85em;
		font-weight: 500;
		color: var(--muted, #898781);
		margin-left: 0.3em;
	}
	dd.good {
		color: var(--good, #0ca30c);
	}
	dd.warn {
		color: var(--warn, #fab219);
	}
	dd.crit {
		color: var(--crit, #d03b3b);
	}
	i {
		display: inline-block;
		width: 0.62em;
		height: 0.62em;
		border-radius: 50%;
		margin-right: 0.5em;
		background: var(--axis, #383835);
	}
	i.lit {
		background: #52ff7d;
		box-shadow: 0 0 0.5em #52ff7d;
	}
</style>
