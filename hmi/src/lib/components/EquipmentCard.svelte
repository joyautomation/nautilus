<script lang="ts" module>
	import type { Quality as _Quality } from '../types.js';
	import type { ChipKind as _ChipKind } from './StateChip.svelte';

	/** One key value on a card. One to three; four is a faceplate. */
	export interface CardValue {
		/** The eyebrow above the number. */
		label?: string;
		value?: number | string | boolean | null;
		units?: string;
		precision?: number;
		/** From `RealtimeClient.quality(tag)` — defaults to the card's own. */
		quality?: _Quality;
		simulated?: boolean;
		present?: boolean;
		ageMs?: number;
	}

	/** One state chip on a card. */
	export interface CardChip {
		label: string;
		kind?: _ChipKind;
		title?: string;
	}
</script>

<script lang="ts">
	// The equipment card — what a schematic becomes when the screen is too
	// narrow to hold it.
	//
	// The source paradigm this reproduces is `Page/ZoneGeneral`: below the
	// design width, the fixed coordinate plane is replaced by one wrapping grid
	// of cards, one per tag, and tapping a card opens that equipment. Three
	// deliberate divergences from the original, each earning its keep:
	//
	//   1. VALUES AND CHIPS ARE ON THE CARD. The source showed a bare symbol
	//      and put every fact inside it; on a phone that is unreadable.
	//   2. THE WHOLE CARD IS THE TAP TARGET, not a nested label — a thumb is
	//      not a mouse pointer.
	//   3. QUALITY DRIVES THE BORDER (`--q-notpublished` solid, `--q-simulated`
	//      dashed), so a card can never show a confident number for a point
	//      the runtime is not publishing. This is the legacy screen's own
	//      magenta/orange border convention, re-expressed as tokens.
	import type { Snippet } from 'svelte';
	import EquipSymbol from './EquipSymbol.svelte';
	import StateChip from './StateChip.svelte';
	import ValueText from './ValueText.svelte';
	import { STATUS_META, valueStatus } from '../quality.js';
	import type { Quality } from '../types.js';
	// `CardValue` / `CardChip` come from the module block above — a module-block
	// declaration is in scope for the instance script.

	let {
		label,
		description = '',
		tag = '',
		src = '',
		symbolWidth = 72,
		symbolHeight = 64,
		running = false,
		fault = false,
		auto,
		remote,
		stateText = '',
		quality = 'good',
		present = true,
		simulated = false,
		values = [],
		chips = [],
		href,
		onopen,
		symbol,
		sparkline,
		extra
	}: {
		/** The equipment's name — the card's title. */
		label: string;
		/** The eyebrow above it. Wraps to two lines, then truncates. */
		description?: string;
		/** The tag path, for the hover title and the accessible name. */
		tag?: string;
		/** Symbol image URL — renders an `EquipSymbol`. Ignored if `symbol`
		 *  is given. */
		src?: string;
		symbolWidth?: number;
		symbolHeight?: number;
		running?: boolean;
		fault?: boolean;
		/** `undefined` hides the A/M chip; same for R/L. */
		auto?: boolean;
		remote?: boolean;
		stateText?: string;
		/** From `RealtimeClient.quality(tag)`. Drives the border. */
		quality?: Quality;
		/** False when the runtime does not publish this equipment at all. */
		present?: boolean;
		/** The equipment's own SIMULATE member. */
		simulated?: boolean;
		/** One to three key values, primary first. */
		values?: CardValue[];
		/** The state chip row. */
		chips?: CardChip[];
		/** Renders the card as a link instead of a button. */
		href?: string;
		/** Tap → open this equipment's faceplate. */
		onopen?: () => void;
		/** Your own symbol, instead of `src`. */
		symbol?: Snippet;
		/** A sparkline under the values, where the family has one. */
		sparkline?: Snippet;
		/** Anything else, at the bottom of the card. */
		extra?: Snippet;
	} = $props();

	const st = $derived(valueStatus({ present, quality, simulated }));
	const meta = $derived(STATUS_META[st]);
	// `EquipSymbol` draws its simulate/comm-fail outline in `--eq-sim`, one
	// token for two different facts. Scoped to this card it becomes the card's
	// own status colour, so the outline and the border never disagree.
	const interactive = $derived(!!onopen || !!href);
	const tagName = $derived(href ? 'a' : onopen ? 'button' : 'div');
</script>

<!-- The click handler only ever exists when `tagName` resolved to `button` or
     `a` (see `tagName` above), so the element is always natively interactive —
     which the compiler cannot see through a dynamic tag. -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<svelte:element
	this={tagName}
	class="eqcard {st}"
	class:interactive
	class:fault
	style="--eq-sim: {meta.token}"
	href={href || undefined}
	type={tagName === 'button' ? 'button' : undefined}
	title={tag || undefined}
	aria-label={interactive ? `${label}${tag ? ` (${tag})` : ''}` : undefined}
	onclick={onopen}
>
	<div class="head">
		<div class="titles">
			{#if description}<span class="eyebrow desc">{description}</span>{/if}
			<span class="name">{label}</span>
		</div>
		<!-- The bell sits at the eyebrow's right, where the source put it. -->
		<span class="bell" class:on={fault} class:blinking={fault} aria-hidden="true">
			{fault ? '🔔' : '🔕'}
		</span>
	</div>

	<div class="body">
		<div class="sym">
			{#if symbol}
				{@render symbol()}
			{:else if src}
				<EquipSymbol
					{src}
					alt={label}
					width={symbolWidth}
					height={symbolHeight}
					{running}
					auto={auto ?? false}
					remote={remote ?? false}
					{fault}
					simulate={simulated}
					comFail={st === 'notPublished' || st === 'bad'}
					{stateText}
					showLabel={false}
					showChips={auto !== undefined || remote !== undefined}
				/>
			{/if}
		</div>

		<!-- The card states its own quality ONCE — on the border and in the chip
		     row. A per-value badge repeating it is noise; a per-value badge that
		     DIFFERS from it is the whole point, so only that one is drawn. -->
		<div class="facts">
			{#each values.slice(0, 3) as v, i (`${i}:${v.label ?? ''}`)}
				{@const vs = valueStatus({
					present: v.present ?? present,
					quality: v.quality ?? quality,
					simulated: v.simulated ?? simulated
				})}
				<ValueText
					label={v.label}
					value={v.value}
					units={v.units}
					precision={v.precision ?? 1}
					quality={v.quality ?? quality}
					simulated={v.simulated ?? simulated}
					present={v.present ?? present}
					ageMs={v.ageMs}
					size={i === 0 ? 'lg' : 'sm'}
					showBadge={vs !== st}
				/>
			{/each}

			{#if chips.length || st !== 'good'}
				<div class="chips">
					{#if st !== 'good'}
						<StateChip kind={st} label={meta.label} title={meta.description} />
					{/if}
					{#each chips as c, i (`${i}:${c.label}`)}
						<StateChip label={c.label} kind={c.kind ?? 'neutral'} title={c.title} />
					{/each}
				</div>
			{/if}
		</div>
	</div>

	{#if sparkline}
		<div class="spark">{@render sparkline()}</div>
	{/if}
	{#if extra}
		<div class="extra">{@render extra()}</div>
	{/if}
</svelte:element>

<style>
	.eqcard {
		/* Fluid near the source's 295×210 card, floored so three values and a
		   chip row still fit at the narrowest column the grid will make. */
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
		min-height: 168px;
		width: 100%;
		padding: var(--space-3);
		text-align: left;
		font: inherit;
		color: var(--ink);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		transition: background var(--transition), border-color var(--transition);
	}
	.eqcard.interactive {
		cursor: pointer;
	}
	.eqcard.interactive:hover {
		background: var(--hover);
		border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
	}
	.eqcard.interactive:active {
		transform: translateY(1px);
	}
	.eqcard:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}

	/* Quality drives the border — the legacy convention, tokenised. Solid for
	   "not being published", dashed for "simulated": two different facts, two
	   different strokes, so they are told apart without reading the chip. */
	.eqcard.notPublished {
		border: 2px solid var(--q-notpublished);
	}
	.eqcard.bad {
		border: 2px solid var(--q-bad);
	}
	.eqcard.simulated {
		border: 2px dashed var(--q-simulated);
	}
	.eqcard.stale {
		border: 1px solid var(--q-stale);
	}
	/* A card in alarm gets a wash, not a fifth border style — the border is
	   already spoken for by quality. */
	.eqcard.fault {
		background: color-mix(in srgb, var(--crit) 8%, var(--surface));
	}

	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: var(--space-1);
		min-width: 0;
	}
	.titles {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.desc {
		/* Two lines max, then ellipsis — the source's 40px label box. */
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	.name {
		font-size: var(--font-sm);
		font-weight: var(--weight-numeric);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.bell {
		flex: none;
		font-size: 13px;
		line-height: 1;
		filter: grayscale(1) opacity(0.45);
	}
	.bell.on {
		filter: none;
	}

	.body {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
		flex: 1;
	}
	.sym {
		flex: none;
		display: flex;
		align-items: center;
	}
	.sym:empty {
		display: none;
	}
	.facts {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
		min-width: 0;
		flex: 1;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1);
		min-width: 0;
	}
	.spark,
	.extra {
		min-width: 0;
	}
</style>
