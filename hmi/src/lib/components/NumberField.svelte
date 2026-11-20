<script lang="ts">
	// Operator numeric entry: edits locally, writes only on Enter / Set.
	// Shows a pending highlight while the entered value differs from live.
	// Given the `tag` it writes, a role that does not hold write on it sees
	// the field read-only, and why (#708; a convenience: the controller decides).
	import { deniedFor } from '../auth.svelte.js';
	let {
		label,
		unit = '',
		value,
		min,
		max,
		step = 1,
		tag,
		onsubmit
	}: {
		label: string;
		unit?: string;
		value: number;
		min?: number;
		max?: number;
		step?: number;
		/** The tag `onsubmit` writes: makes the field follow the signed-in role. */
		tag?: string;
		onsubmit: (v: number) => void;
	} = $props();

	const denied = $derived(tag ? deniedFor('write', tag) : '');

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
			readonly={!!denied}
			title={denied || undefined}
			oninput={(e) => {
				editing = true;
				text = (e.currentTarget as HTMLInputElement).value;
			}}
			onkeydown={(e) => e.key === 'Enter' && commit()}
			onblur={() => {
				if (!pending) editing = false;
			}}
		/>
		<button onclick={commit} disabled={!pending || !!denied}>Set</button>
	</span>
</label>

<style>
	.field {
		display: block;
	}
	.lab {
		display: block;
		font-size: var(--font-2xs);
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
