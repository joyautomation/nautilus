<script lang="ts">
	// A momentary operator pushbutton — Start / Stop / Open / Close / Reset.
	//
	// The real command in a PLC is a pulse, not a level: write true, then false
	// a few hundred milliseconds later, and let the logic latch on the edge.
	// `pulseMs = 0` latches instead, for a command bit the program clears
	// itself.
	//
	// Same injected-write contract as `WriteNumber` / `WriteToggle`:
	// `write(name, value)` resolves to `null` on success or a refusal reason,
	// which is rendered beside the button rather than swallowed.
	let {
		tag,
		label,
		kind = 'neutral',
		pulseMs = 400,
		held = false,
		disabled = false,
		disabledReason = '',
		write
	}: {
		/** Tag path handed to `write`. */
		tag: string;
		label: string;
		/** Colour role. `start`/`stop` borrow the good/critical roles; they are
		 *  never the only cue — the label always says what the button does. */
		kind?: 'start' | 'stop' | 'neutral';
		/** Momentary: write true, then false after this many ms. 0 = latch true. */
		pulseMs?: number;
		/** The live readback of the command bit, for the pressed look. */
		held?: boolean;
		disabled?: boolean;
		/** Why it is disabled, shown as the button's title. */
		disabledReason?: string;
		/** Resolves to `null` on success, or the controller's refusal reason. */
		write: (name: string, value: boolean) => Promise<string | null>;
	} = $props();

	let error = $state('');
	let busy = $state(false);

	async function press() {
		if (disabled || busy) return;
		busy = true;
		const reason = await write(tag, true);
		if (reason) {
			error = reason;
			busy = false;
			return;
		}
		error = '';
		if (pulseMs > 0) {
			setTimeout(async () => {
				await write(tag, false);
				busy = false;
			}, pulseMs);
		} else {
			busy = false;
		}
	}
</script>

<button
	type="button"
	class="cmd {kind}"
	class:held
	{disabled}
	onclick={press}
	title={disabled ? disabledReason : `Pulse ${tag}`}
>
	{label}
</button>
{#if error}<span class="err">{error}</span>{/if}

<style>
	.cmd {
		padding: 6px 12px;
		border-radius: 3px;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--ink);
		font: inherit;
		font-size: var(--font-xs);
		font-weight: 600;
		cursor: pointer;
	}

	.cmd.start {
		border-color: color-mix(in srgb, var(--good) 60%, var(--border));
		color: color-mix(in srgb, var(--good) 75%, var(--ink));
	}

	.cmd.stop {
		border-color: color-mix(in srgb, var(--crit) 60%, var(--border));
		color: color-mix(in srgb, var(--crit) 75%, var(--ink));
	}

	.cmd:hover:not(:disabled) {
		background: var(--hover);
	}

	.cmd.held {
		background: color-mix(in srgb, var(--accent) 20%, var(--surface));
	}

	.cmd:disabled {
		opacity: 0.45;
		cursor: not-allowed;
	}

	.err {
		font-size: 9px;
		color: var(--crit);
	}
</style>
