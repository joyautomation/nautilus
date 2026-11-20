<script lang="ts">
	// A standby's referral the kit did not follow (#708): a second one within
	// the loop guard, a browser without sessionStorage, or a leader URL that
	// may not be opened. Put it at the top of the page. It says what the
	// controller said and, when it named a leader that may be opened, links
	// there for the person to click. Nothing otherwise.
	import { currentAuth } from '../auth.svelte.js';

	const referral = $derived(currentAuth()?.referral ?? null);
</script>

{#if referral}
	<div class="banner" role="alert">
		<span>{referral.message}</span>
		{#if referral.leader}<a href={referral.leader}>Open the leader</a>{/if}
	</div>
{/if}

<style>
	.banner {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 10px;
		padding: 8px 12px;
		border: 1px solid var(--serious, var(--border));
		border-radius: var(--radius, 8px);
		background: var(--surface-2);
		color: var(--ink);
		font-size: var(--font-xs);
	}
	a {
		color: var(--accent);
		font-weight: 600;
	}
</style>
