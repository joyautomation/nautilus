<script lang="ts">
	// An operator setpoint field bound to one tag path.
	//
	// The write itself is injected: `write(name, value)` resolves to `null` on
	// success or a REASON string on refusal — the same contract as
	// `RealtimeClient.writeTag`, so the common case is `write={rt.writeTag}`.
	// A refusal is shown on the control rather than swallowed, because a
	// faceplate that silently fails to command is worse than one that says it
	// cannot.
	//
	// The field is optimistic only while the operator is typing: `value` (the
	// live readback) drives the display, and the moment an edit is in flight
	// the field is marked `pending` so the operator can see their number has
	// not been confirmed by the controller yet.
	let {
		tag,
		label,
		value,
		present = true,
		units = '',
		precision = 1,
		step = 1,
		min,
		max,
		readonly = false,
		readonlyReason = '',
		write
	}: {
		/** Tag path handed to `write` — a whole tag or a dotted member path. */
		tag: string;
		label: string;
		/** The live readback. `undefined`/non-finite renders blank, never 0. */
		value?: number;
		/** False when the runtime does not publish this tag at all. */
		present?: boolean;
		units?: string;
		precision?: number;
		step?: number;
		min?: number;
		max?: number;
		/** Display-only — the control renders greyed with `readonlyReason`. */
		readonly?: boolean;
		/** Why it is read-only, shown as the field's title. */
		readonlyReason?: string;
		/** Resolves to `null` on success, or the controller's refusal reason. */
		write: (name: string, value: number) => Promise<string | null>;
	} = $props();

	const live = $derived(typeof value === 'number' && Number.isFinite(value) ? value : NaN);
	const canWrite = $derived(!readonly);

	let editing = $state(false);
	let text = $state('');
	let error = $state('');

	const shown = $derived(editing ? text : Number.isFinite(live) ? live.toFixed(precision) : '');
	const pending = $derived(editing && text !== '' && Number(text) !== live);

	async function commit() {
		if (!editing || text === '') {
			editing = false;
			return;
		}
		const v = Number(text);
		if (!Number.isFinite(v)) {
			editing = false;
			return;
		}
		error = (await write(tag, v)) ?? '';
		editing = false;
	}
</script>

<label class="f" class:pending class:ro={!canWrite}>
	<span class="lab">{label}{units ? ` (${units})` : ''}</span>
	<input
		type="number"
		{step}
		{min}
		{max}
		value={shown}
		readonly={!canWrite}
		disabled={!present}
		placeholder={present ? '' : '—'}
		title={canWrite ? '' : readonlyReason}
		oninput={(e) => {
			editing = true;
			text = (e.currentTarget as HTMLInputElement).value;
		}}
		onkeydown={(e) => e.key === 'Enter' && commit()}
		onblur={() => !pending && (editing = false)}
	/>
	{#if error}<span class="err">{error}</span>{/if}
</label>

<style>
	.f {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.lab {
		font-size: var(--font-2xs);
		color: var(--muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	input {
		width: 100%;
		padding: 4px 6px;
		font-family: var(--mono);
		font-size: var(--font-xs);
		background: var(--surface);
		color: var(--ink);
		border: 1px solid var(--border);
		border-radius: 3px;
	}

	.ro input {
		background: var(--surface-2);
		color: var(--ink-2);
		cursor: not-allowed;
	}

	.pending input {
		border-color: var(--accent);
		box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 22%, transparent);
	}

	.err {
		font-size: 9px;
		color: var(--crit);
	}
</style>
