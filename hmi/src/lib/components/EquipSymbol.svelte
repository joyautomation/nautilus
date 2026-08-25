<script lang="ts">
	// An image symbol with SCADA state chrome around it.
	//
	// Legacy symbol libraries (Ignition, FactoryTalk, WinCC) are raster art, not
	// vectors, and equipment state is shown by decorating the picture rather
	// than redrawing it: while it runs the metal itself reads green (a coloured
	// outline while it is simulated or off-comms, a fault bell, and A/M · R/L
	// mode chips down the side). This is that wrapper, generic over an image
	// `src` plus booleans — the same chrome for a pump, a valve, a blower or a
	// mixer.
	//
	// SIZING mirrors the `fit: contain` those packages use. Symbol PNGs have
	// wildly different aspect ratios (352×540 for one pump, 532×315 for the
	// next), so a wrapper that takes only `width` renders half of them four
	// times too tall. Give it BOTH `width` and `height` and the picture is
	// clamped on both axes, keeping its own ratio inside the box.
	//
	// RUNNING is drawn as a tint by default — Ignition's own Image component
	// has a `tint` property for exactly this (`#00FF0063`, a translucent green
	// masked to the image's own alpha channel, verified against the Pomona
	// AEP Perspective export's Motor 1 Speed and Globe Valve symbols) — so the
	// metal shading still reads through the colour instead of a flat green
	// silhouette. `runStyle="wash"` restores the older flat translucent box
	// for a port that isn't ready to switch.
	//
	// Colours are CSS custom properties rather than props, so a port can pin
	// them to the source palette once: `--eq-run-tint` (the running tint,
	// `runStyle="tint"`), `--eq-tint` (the running wash, `runStyle="wash"`),
	// `--eq-sim` (the simulate/comm-fail outline), `--eq-chip-bg` /
	// `--eq-chip-ink`.
	import type { Snippet } from 'svelte';

	let {
		src,
		alt = '',
		running = false,
		runStyle = 'tint',
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
		/** Colours the picture while running — a tint of the image itself by
		 *  default, or the older flat wash box via `runStyle="wash"`. */
		running?: boolean;
		/** `'tint'` (default): mask `src` and paint it `--eq-run-tint`, so only
		 *  the equipment's own opaque pixels take the colour — the legacy
		 *  Ignition idiom. `'wash'`: the old flat `--eq-tint` box over the
		 *  whole symbol footprint, kept for back-compat. */
		runStyle?: 'tint' | 'wash';
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
			class:fit={height !== undefined}
			{onclick}
			title={label || alt}
			style:width={height !== undefined ? `${width}px` : undefined}
			style:height={height !== undefined ? `${height}px` : undefined}
		>
			<img
				{src}
				alt={alt || label}
				style:width={height !== undefined ? undefined : `${width}px`}
				style:max-width={height !== undefined ? undefined : `${width}px`}
				class:mirror
			/>
			<span
				class="tint"
				class:on={running}
				class:wash={runStyle === 'wash'}
				class:mode-tint={runStyle === 'tint'}
				class:mirror={runStyle === 'tint' && mirror}
				style:mask-image={runStyle === 'tint' ? `url(${JSON.stringify(src)})` : undefined}
				style:-webkit-mask-image={runStyle === 'tint' ? `url(${JSON.stringify(src)})` : undefined}
				aria-hidden="true"
			></span>
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

	/* Both `width` and `height` given: the symbol gets a REAL box and the
	   picture is `contain`-fit inside it, keeping its own ratio.
	   The box has to be explicit rather than left to `max-width`/`max-height`
	   on the picture: a raster/SVG symbol whose file carries only a viewBox
	   contributes a min-content width of zero to a flex row, so the wrapper
	   collapsed and the symbol rendered as nothing at all the moment it sat
	   next to anything else (a chip column, a card's value stack). */
	.img.fit {
		display: grid;
		/* Declared 1fr tracks make the grid area definite so the img's
		   percentage max-height resolves; with the default auto row it is
		   circular and browsers resolve it as none — wide images then size
		   by width alone and spill vertically past a given height. */
		grid-template-rows: minmax(0, 1fr);
		grid-template-columns: minmax(0, 1fr);
		place-items: center;
		flex: none;
	}

	.img.fit img {
		width: auto;
		height: auto;
		max-width: 100%;
		max-height: 100%;
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
		transition:
			background 0.3s ease,
			opacity 0.3s ease;
	}

	.tint.mirror {
		transform: scaleX(-1);
	}

	/* runStyle="wash" (back-compat): a flat translucent box over the whole
	   symbol footprint, independent of the picture's own shape. */
	.tint.wash.on {
		background: var(--eq-tint, var(--eq-state-run-wash, color-mix(in srgb, var(--good) 39%, transparent)));
	}

	/* runStyle="tint" (default): the same picture, masked to its own alpha
	   channel and painted solid, so only the equipment's opaque pixels take
	   the colour and the metal shading still reads through — matching
	   Ignition's own Image `tint` property (`#00FF0063`) rather than a flat
	   green silhouette. `mask-size: contain` + `mask-position: center`
	   reproduce the same "contain" placement the real `<img>` uses, whether
	   sized off `width` alone or `contain`-fit into `width × height`. */
	.tint.mode-tint.on {
		background: var(--eq-run-tint, var(--good));
		opacity: 0.39;
		mask-repeat: no-repeat;
		mask-position: center;
		mask-size: contain;
		-webkit-mask-repeat: no-repeat;
		-webkit-mask-position: center;
		-webkit-mask-size: contain;
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
