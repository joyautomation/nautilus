<script lang="ts">
	// A boolean tag as a lamp plus a switch — the commandable sibling of a
	// pilot light. Same injected-write contract as `WriteNumber`:
	// `write(name, value)` resolves to `null` on success or a refusal reason.
	//
	// `alarm` turns it into a read-only indication: red while set, flashing,
	// with no switch affordance at all. An alarm lamp that can be clicked is a
	// hazard, so the two modes are one prop rather than a convention.
	let {
		tag,
		label,
		value = false,
		present = true,
		onColor = 'var(--good)',
		offColor = 'var(--muted)',
		alarm = false,
		invert = false,
		readonly = false,
		readonlyReason = '',
		write
	}: {
		/** Tag path handed to `write`. */
		tag: string;
		label: string;
		/** The live readback. */
		value?: boolean;
		/** False when the runtime does not publish this tag at all. */
		present?: boolean;
		onColor?: string;
		offColor?: string;
		/** Indication only: red + flashing when set, never clickable. */
		alarm?: boolean;
		/** Display (and command) the logical complement of the raw bit. */
		invert?: boolean;
		/** Display-only — no switch affordance, `readonlyReason` as the title. */
		readonly?: boolean;
		readonlyReason?: string;
		/** Resolves to `null` on success, or the controller's refusal reason. */
		write: (name: string, value: boolean) => Promise<string | null>;
	} = $props();

	const on = $derived(invert ? !value : value);
	const canWrite = $derived(!alarm && !readonly);

	let error = $state('');

	async function toggle() {
		if (!canWrite) return;
		error = (await write(tag, !value)) ?? '';
	}

	const color = $derived(alarm ? (on ? 'var(--crit)' : offColor) : on ? onColor : offColor);
</script>

<svelte:element
	this={canWrite ? 'button' : 'div'}
	type={canWrite ? 'button' : undefined}
	role={canWrite ? 'button' : undefined}
	class="t"
	class:on
	class:clickable={canWrite}
	class:absent={!present}
	style:--c={color}
	onclick={canWrite ? toggle : undefined}
	title={canWrite ? `Write ${!value} to ${tag}` : alarm ? tag : readonlyReason}
>
	<span class="lamp" class:blinking={alarm && on} aria-hidden="true"></span>
	<span class="lab">{label}</span>
	{#if !present}<span class="q">—</span>{/if}
	{#if error}<span class="err" title={error}>!</span>{/if}
</svelte:element>

<style>
	.t {
		display: flex;
		align-items: center;
		gap: 7px;
		width: 100%;
		padding: 4px 7px;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 3px;
		font: inherit;
		font-size: var(--font-2xs);
		color: var(--ink-2);
		text-align: left;
	}

	.t.on {
		background: color-mix(in srgb, var(--c) 12%, var(--surface));
		border-color: color-mix(in srgb, var(--c) 55%, var(--border));
		color: var(--ink);
		font-weight: 600;
	}

	.clickable {
		cursor: pointer;
	}

	.clickable:hover {
		background: var(--hover);
	}

	.absent {
		opacity: 0.5;
	}

	.lamp {
		flex: none;
		width: 10px;
		height: 10px;
		border-radius: 50%;
		background: var(--c);
		border: 1px solid rgba(0, 0, 0, 0.35);
		box-shadow: inset 0 1px 1px rgba(255, 255, 255, 0.5);
	}

	.lab {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.q,
	.err {
		font-family: var(--mono);
		color: var(--muted);
	}

	.err {
		color: var(--crit);
	}
</style>
