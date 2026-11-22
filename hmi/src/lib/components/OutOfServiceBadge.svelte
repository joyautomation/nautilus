<script lang="ts">
	// The badge on a suppressed alarm: what kind of suppression, why, and for
	// an equipment out of service who took it out and until when (#715). The
	// words are on the badge, not only in a tooltip: a hidden alarm must say so.
	import { badgeText, suppressionBadge, type SuppressedFields } from '../outofservice.js';

	let { alarm }: { alarm: SuppressedFields } = $props();

	const badge = $derived(suppressionBadge(alarm));
	const fmt = (ms: number) =>
		new Date(ms).toLocaleString(undefined, { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' });
</script>

{#if badge}
	<span class="oos" class:timed={badge.untilMs > 0} title={badgeText(badge, fmt)} data-suppression={alarm.suppression ?? ''}>
		<span class="label">{badge.label}</span>
		{#if badge.why}<span class="why">{badge.why}</span>{/if}
		{#if badge.who}<span class="who">by {badge.who}</span>{/if}
		{#if badge.untilMs}<span class="until">until {fmt(badge.untilMs)}</span>{/if}
	</span>
{/if}

<style>
	.oos {
		display: inline-flex;
		flex-wrap: wrap;
		gap: 0 var(--space-2);
		align-items: baseline;
		font-size: var(--font-2xs);
		color: var(--muted);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		padding: 0 var(--space-1);
	}
	.label {
		font-weight: 600;
		color: var(--ink-2);
	}
	.why {
		color: var(--ink);
	}
</style>
