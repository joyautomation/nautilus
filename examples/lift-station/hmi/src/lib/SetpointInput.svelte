<script lang="ts">
	// A compact setpoint entry for the panels: a small number box with its
	// unit. It shows the live value until you type; Enter (or the Set
	// button that appears while the box differs from live) writes it, and
	// Escape puts the live value back. The same rule as the kit's
	// NumberField, in a size that fits several to a row.
	let {
		value,
		unit = '',
		label,
		min,
		max,
		step = 1,
		onsubmit
	}: {
		value: number;
		unit?: string;
		/** Accessible name: the box has no visible label of its own. */
		label: string;
		min?: number;
		max?: number;
		step?: number;
		onsubmit: (v: number) => void;
	} = $props();

	let editing = $state(false);
	let text = $state('');
	const shown = $derived(editing ? text : String(value));
	const pending = $derived(editing && text !== '' && Number(text) !== value);

	function commit() {
		const v = Number(text);
		if (!pending || !isFinite(v)) return;
		onsubmit(Math.min(max ?? v, Math.max(min ?? v, v)));
		editing = false;
	}
	function revert() {
		editing = false;
	}
</script>

<span class="sp" class:pending>
	<input
		type="number"
		aria-label={label}
		value={shown}
		{min}
		{max}
		{step}
		oninput={(e) => {
			editing = true;
			text = e.currentTarget.value;
		}}
		onkeydown={(e) => {
			if (e.key === 'Enter') commit();
			if (e.key === 'Escape') revert();
		}}
		onblur={() => !pending && revert()}
	/>
	{#if unit}<span class="u">{unit}</span>{/if}
	{#if pending}<button onclick={commit} title="Write it (Enter)">Set</button>{/if}
</span>

<style>
	.sp {
		display: inline-flex;
		align-items: center;
		gap: 4px;
	}
	input {
		width: 4.2em;
		padding: 4px 6px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg);
		color: var(--ink);
		font: inherit;
		font-family: var(--mono);
		font-size: 0.82rem;
		font-variant-numeric: tabular-nums;
		text-align: right;
	}
	input::-webkit-inner-spin-button {
		display: none;
	}
	input {
		-moz-appearance: textfield;
		appearance: textfield;
	}
	.pending input {
		border-color: var(--s1);
	}
	.u {
		min-width: 1.4em;
		font-size: 0.75rem;
		color: var(--muted);
	}
	button {
		padding: 3px 8px;
		border: 1px solid var(--s1);
		border-radius: 6px;
		background: var(--s1);
		color: var(--bg);
		font: inherit;
		font-size: 0.72rem;
		font-weight: 650;
		cursor: pointer;
	}
</style>
