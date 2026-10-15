<script lang="ts">
	// One priority's sign, drawn: the shape and mark PRIORITY_SIGN gives it,
	// in the priority's colour. The banner, the alarm table and the 3D kit's
	// markers all draw this, so a level has one shape everywhere. Pure SVG.
	import { PRIORITY_META, PRIORITY_SIGN, type Priority } from '../alarms.svelte.js';

	let {
		priority,
		color,
		size = 16,
		hollow = false,
		ink = 'var(--surface, #111)',
		title
	}: {
		priority: string;
		/** The fill; the priority's palette role when omitted. */
		color?: string;
		/** Square size in px. */
		size?: number;
		/** Returned to normal, waiting to be acknowledged: outlined, not filled. */
		hollow?: boolean;
		/** The mark and the outline: the surface the sign sits on, so it reads in either theme. */
		ink?: string;
		/** Accessible name; `<priority>: <urgency>` when omitted. */
		title?: string;
	} = $props();

	let sign = $derived(PRIORITY_SIGN[priority] ?? PRIORITY_SIGN.high);
	let fill = $derived(color ?? PRIORITY_META[priority as Priority]?.color ?? PRIORITY_META.high.color);
	// Every shape in a 24-unit box, drawn a little inside it for the stroke.
	const SHAPES: Record<string, string> = {
		octagon: 'M8 2h8l6 6v8l-6 6H8l-6-6V8z',
		triangle: 'M12 2.5 22.5 21h-21z',
		diamond: 'M12 1.5 22.5 12 12 22.5 1.5 12z',
		circle: 'M12 2a10 10 0 1 0 0.001 0z',
		square: 'M3 3h18v18H3z'
	};
	let ty = $derived(sign.shape === 'triangle' ? 17 : 16);
</script>

<svg class="sign" width={size} height={size} viewBox="0 0 24 24" role="img" aria-label={title ?? `${priority}: ${sign.urgency}`}>
	<path
		d={SHAPES[sign.shape]}
		style:fill={hollow ? ink : fill}
		style:fill-opacity={hollow ? 0.55 : 1}
		style:stroke={hollow ? fill : ink}
		stroke-width={hollow ? 2 : 1.2}
		stroke-linejoin="round"
	/>
	<text
		x="12"
		y={ty}
		text-anchor="middle"
		font-family="system-ui, sans-serif"
		font-weight="800"
		font-size={sign.glyph.length > 1 ? 10 : 12}
		style:fill={hollow ? fill : ink}>{sign.glyph}</text
	>
</svg>

<style>
	.sign {
		flex: none;
		display: inline-block;
		vertical-align: middle;
	}
</style>
