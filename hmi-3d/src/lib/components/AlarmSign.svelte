<script lang="ts">
	// One priority's sign, drawn: the shape and glyph PRIORITY_SIGN gives
	// it, in the palette's colour for the level. The marker floats it in
	// the scene; the key lists it. Pure SVG, no scene needed.
	import { PRIORITY_SIGN } from '../alarms.js';

	let { priority, color, size = 22 }: { priority: string; color: string; size?: number } = $props();
	let sign = $derived(PRIORITY_SIGN[priority] ?? PRIORITY_SIGN.high);
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

<svg width={size} height={size} viewBox="0 0 24 24" role="img" aria-label={`${priority}: ${sign.urgency}`}>
	<path d={SHAPES[sign.shape]} fill={color} stroke="#111" stroke-width="1.2" stroke-linejoin="round" />
	<text x="12" y={ty} text-anchor="middle" font-family="system-ui, sans-serif" font-weight="800" font-size={sign.glyph.length > 1 ? 10 : 12} fill="#111">{sign.glyph}</text>
</svg>
