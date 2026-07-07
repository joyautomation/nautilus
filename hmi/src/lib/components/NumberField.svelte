<script lang="ts">
	// Operator numeric entry: edits locally, writes only on Enter / Set.
	// Shows a pending highlight while the entered value differs from live.
	let {
		label,
		unit = '',
		value,
		min,
		max,
		step = 1,
		onsubmit
	}: {
		label: string;
		unit?: string;
		value: number;
		min?: number;
		max?: number;
		step?: number;
		onsubmit: (v: number) => void;
	} = $props();

	let text = $state('');
	let editing = $state(false);

	let display = $derived(editing ? text : String(value));
	let pending = $derived(editing && text !== '' && Number(text) !== value);

	function commit() {
		const v = Number(text);
		if (!editing || text === '' || Number.isNaN(v)) {
			editing = false;
			return;
		}
		onsubmit(v);
		editing = false;
	}
</script>

<label class="field" class:pending>
	<span class="lab">{label}{unit ? ` (${unit})` : ''}</span>
	<span class="row">
		<input
			type="number"
			{min}
			{max}
			{step}
			value={display}
			oninput={(e) => {
				editing = true;
				text = (e.currentTarget as HTMLInputElement).value;
			}}
			onkeydown={(e) => e.key === 'Enter' && commit()}
			onblur={() => {
				if (!pending) editing = false;
			}}
		/>
		<button onclick={commit} disabled={!pending}>Set</button>
	</span>
</label>

<style>
	.field {
		display: block;
	}
	.lab {
		display: block;
		font-size: 11px;
		color: var(--muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
		margin-bottom: 4px;
	}
	.row {
		display: flex;
		gap: 6px;
	}
	.pending input {
		border-color: var(--s2);
	}
	button:disabled {
		opacity: 0.45;
		cursor: default;
	}
</style>
