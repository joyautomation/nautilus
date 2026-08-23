<script lang="ts">
	// An image symbol with SCADA state chrome around it.
	//
	// Legacy symbol libraries (Ignition, FactoryTalk, WinCC) are raster art, not
	// vectors, and equipment state is shown by decorating the picture rather
	// than redrawing it: a translucent wash while it runs, a coloured outline
	// while it is simulated or off-comms, a fault bell, and A/M · R/L mode
	// chips down the side. This is that wrapper, generic over an image `src`
	// plus booleans — the same chrome for a pump, a valve, a blower or a mixer.
	//
	// SIZING mirrors the `fit: contain` those packages use. Symbol PNGs have
	// wildly different aspect ratios (352×540 for one pump, 532×315 for the
	// next), so a wrapper that takes only `width` renders half of them four
	// times too tall. Give it BOTH `width` and `height` and the picture is
	// clamped on both axes, keeping its own ratio inside the box.
	//
	// Colours are CSS custom properties rather than props, so a port can pin
	// them to the source palette once: `--eq-tint` (the run wash), `--eq-sim`
	// (the simulate/comm-fail outline), `--eq-chip-bg` / `--eq-chip-ink`.
	import type { Snippet } from 'svelte';

	let {
		src,
		alt = '',
		running = false,
		auto = false,
		remote = false,
		fault = false,
		simulate = false,
		comFail = false,
		stateText = '',
		label = '',
		showLabel = true,
		showChips = true,
		width = 82,
		height,
		mirror = false,
		onclick,
		extra
	}: {
		/** URL of the symbol image. */
		src: string;
		alt?: string;
		/** Lays the `--eq-tint` wash over the picture. */
		running?: boolean;
		/** Auto vs manual — renders the first chip as `A` or `M`. */
		auto?: boolean;
		/** Remote vs local — renders the second chip as `R` or `L`. */
		remote?: boolean;
		/** Any-fault: rings the bell and flashes it. */
		fault?: boolean;
		/** Value is simulated — outlines the symbol in `--eq-sim`. */
		simulate?: boolean;
		/** Communications failed — same outline as `simulate`. */
		comFail?: boolean;
		/** The word above the symbol: On/Off, Open/Closed/Transition/Error… */
		stateText?: string;
		/** Caption under the symbol, and the hover title. */
		label?: string;
		showLabel?: boolean;
		/** Hide the A/M and R/L chips for a symbol with no mode. */
		showChips?: boolean;
		/** Box width, px. */
		width?: number;
		/** Box height, px — set it and the picture is `contain`-fit into
		 *  `width × height` instead of sizing off `width` alone. */
		height?: number;
		/** Flip horizontally, for a symbol drawn facing the other way. */
		mirror?: boolean;
		/** Makes the symbol a button — usually "open the faceplate". */
		onclick?: () => void;
		/** Extra chrome under the symbol (a value pill, a speed readout). */
		extra?: Snippet;
	} = $props();
</script>

<div class="eq">
	<div class="state">{stateText}</div>

	<div class="body">
		<span class="bell" class:on={fault} class:blinking={fault} aria-hidden="true">
			{fault ? '🔔' : '🔕'}
		</span>

		<svelte:element
			this={onclick ? 'button' : 'div'}
			type={onclick ? 'button' : undefined}
			role={onclick ? 'button' : undefined}
			class="img"
			class:clickable={!!onclick}
			class:sim={simulate || comFail}
			{onclick}
			title={label || alt}
		>
			<img
				{src}
				alt={alt || label}
				style:width={height ? 'auto' : `${width}px`}
				style:max-width={`${width}px`}
				style:max-height={height ? `${height}px` : undefined}
				class:mirror
			/>
			<span class="tint" class:on={running} aria-hidden="true"></span>
		</svelte:element>

		{#if showChips}
			<div class="chips">
				<span class="chip">{auto ? 'A' : 'M'}</span>
				<span class="chip">{remote ? 'R' : 'L'}</span>
			</div>
		{/if}
	</div>

	{#if extra}<div class="extra">{@render extra()}</div>{/if}
	{#if showLabel && label}<div class="name">{label}</div>{/if}
</div>

<style>
	.eq {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 3px;
	}

	.state {
		font-size: 0.8rem;
		min-height: 17px;
		color: var(--ink);
	}

	.body {
		display: flex;
		align-items: center;
		gap: 4px;
	}

	.bell {
		font-size: 15px;
		filter: grayscale(1) opacity(0.45);
		line-height: 1;
	}

	.bell.on {
		filter: none;
	}

	.img {
		position: relative;
		display: block;
		padding: 0;
		border: 3px solid transparent;
		border-radius: 10px;
		background: none;
		line-height: 0;
	}

	/* simulate / comm-fail outline */
	.img.sim {
		border-color: var(--eq-sim, var(--warn));
	}

	.clickable {
		cursor: pointer;
	}

	.img img {
		height: auto;
		display: block;
	}

	.img img.mirror {
		transform: scaleX(-1);
	}

	.tint {
		position: absolute;
		inset: 0;
		border-radius: 7px;
		background: transparent;
		pointer-events: none;
		transition: background 0.3s ease;
	}

	.tint.on {
		background: var(--eq-tint, color-mix(in srgb, var(--good) 39%, transparent));
	}

	.chips {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.chip {
		width: 26px;
		height: 26px;
		display: grid;
		place-items: center;
		background: var(--eq-chip-bg, var(--surface-2));
		border-radius: 3px;
		box-shadow: rgba(0, 0, 0, 0.2) 0 2px 4px 0;
		font-size: 0.8rem;
		font-weight: 500;
		color: var(--eq-chip-ink, var(--ink));
	}

	.name {
		font-size: var(--font-2xs);
		color: var(--ink-2);
		text-align: center;
		max-width: 150px;
	}

	.extra {
		display: flex;
		gap: 6px;
	}
</style>
