<script lang="ts">
	// Status indicator: reserved status colors, always icon + label (never color alone).
	import type { StatusKind } from '../types.js';

	let { kind = 'off', label }: { kind?: StatusKind; label: string } = $props();

	const icons: Record<StatusKind, string> = {
		good: '●',
		warning: '▲',
		serious: '▲',
		critical: '■',
		off: '○'
	};
	const colors: Record<StatusKind, string> = {
		good: 'var(--good)',
		warning: 'var(--warn)',
		serious: 'var(--serious)',
		critical: 'var(--crit)',
		off: 'var(--muted)'
	};
</script>

<span class="pill" style="--c: {colors[kind]}">
	<span class="icon" aria-hidden="true">{icons[kind]}</span>
	{label}
</span>

<style>
	.pill {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 3px 10px;
		border: 1px solid var(--border);
		border-radius: 999px;
		font-size: 12px;
		font-weight: 550;
		color: var(--ink-2);
		background: var(--surface-2);
		white-space: nowrap;
	}
	.icon {
		color: var(--c);
		font-size: 10px;
	}
</style>
