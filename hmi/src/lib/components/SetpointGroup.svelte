<script lang="ts">
	// Setpoints grouped with what they set: a titled panel of fields, each
	// one a WriteNumber writing its own tag. It is as tall as its fields
	// need (give it a width, never a height), so it can't spill fields out
	// of its box, and each label sits above its field, never on it.
	//
	// On a mimic, <Mimic> hands it the live tags, so `items` only names them:
	//   { "component": "SetpointGroup", "x": 40, "y": 860, "width": 300,
	//     "props": { "title": "Backwash triggers", "items": [
	//       { "tag": "BW_HeadlossSP", "label": "High headloss", "unit": "ft" },
	//       { "tag": "BW_RunHoursSP", "label": "Run time", "unit": "h", "decimals": 0 } ] } }
	// In an app, pass `tags` (the frame's tag values) and `write` yourself.
	import type { MimicLook } from '../mimic.js';
	import WriteNumber from './WriteNumber.svelte';

	type Item = { tag: string; label?: string; unit?: string; decimals?: number; step?: number; min?: number; max?: number };
	let {
		title = '',
		items = [],
		columns = 1,
		tags = {},
		write,
		width,
		label,
		look = 'isa101'
	}: {
		title?: string;
		items?: Item[];
		/** Fields side by side: 1 (default) or more. */
		columns?: number;
		/** The live tag values, by name. */
		tags?: Record<string, unknown>;
		write?: (name: string, value: unknown) => Promise<string | null>;
		width?: number;
		/** The equipment's label, used as the title when `title` is empty. */
		label?: string;
		look?: MimicLook;
	} = $props();

	const value = (t: string) => {
		const v = tags[t];
		const n = v && typeof v === 'object' && 'value' in v ? (v as { value: unknown }).value : v;
		return typeof n === 'number' ? n : Number(n);
	};
	const noWrite = async () => 'This screen is read-only';
	// With no `decimals`, a field shows its value as precisely as it is (up
	// to 4 places): a gain of 4.25 reads 4.25 and 0.07 reads 0.07, never
	// rounded to a number the controller doesn't hold.
	const places = (it: Item): number => {
		if (it.decimals !== undefined) return it.decimals;
		const v = value(it.tag);
		if (!Number.isFinite(v)) return 1;
		for (let d = 0; d < 4; d++) if (Math.abs(Number(v.toFixed(d)) - v) < 1e-9) return d;
		return 4;
	};
</script>

<section class="sg" style:width={width ? `${width}px` : undefined} aria-label={title || label || 'Setpoints'}>
	{#if title || label}<h3>{title || label}</h3>{/if}
	<div class="fields" style:grid-template-columns="repeat({Math.max(1, columns)}, minmax(0, 1fr))">
		{#each items as it, i (i)}
			<WriteNumber
				tag={it.tag}
				label={it.label ?? it.tag}
				units={it.unit ?? ''}
				value={value(it.tag)}
				present={it.tag in tags}
				precision={places(it)}
				step={it.step ?? 10 ** -(it.decimals ?? 1)}
				min={it.min}
				max={it.max}
				write={write ?? noWrite}
			/>
		{/each}
	</div>
</section>

<style>
	.sg {
		box-sizing: border-box;
		display: grid;
		gap: 8px;
		padding: 10px 12px 12px;
		background: var(--surface, #161615);
		border: 1px solid var(--border, #2a2a28);
		border-radius: 6px;
		min-width: 160px;
	}
	h3 {
		margin: 0;
		font-size: 12px;
		font-weight: 700;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--ink-2, #c3c2b7);
	}
	.fields {
		display: grid;
		gap: 8px 12px;
	}
	/* the field's own label at a size an operator reads */
	.fields :global(.lab) {
		font-size: 12px;
		color: var(--ink-2, #c3c2b7);
	}
</style>
