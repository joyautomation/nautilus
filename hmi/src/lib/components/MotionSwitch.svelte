<script lang="ts">
	// Three-way motion selector (System / Full / Reduced), parallel to the
	// theme switch.
	import { motion, type Motion } from '../motion.svelte.js';

	const options: { value: Motion; label: string; icon: 'system' | 'full' | 'reduced' }[] = [
		{ value: 'system', label: 'System motion', icon: 'system' },
		{ value: 'full', label: 'Full motion', icon: 'full' },
		{ value: 'reduced', label: 'Reduced motion', icon: 'reduced' }
	];
</script>

<div class="motion-switch" role="group" aria-label="Motion">
	{#each options as o}
		<button
			type="button"
			class="opt"
			class:active={motion.value === o.value}
			onclick={() => motion.set(o.value)}
			title={o.label}
			aria-label={o.label}
			aria-pressed={motion.value === o.value}
		>
			{#if o.icon === 'system'}
				<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<rect x="2" y="3" width="20" height="14" rx="2" /><line x1="8" y1="21" x2="16" y2="21" /><line x1="12" y1="17" x2="12" y2="21" />
				</svg>
			{:else if o.icon === 'full'}
				<!-- activity pulse = motion on -->
				<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M22 12h-4l-3 9L9 3l-3 9H2" />
				</svg>
			{:else}
				<!-- pause = motion reduced -->
				<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" />
				</svg>
			{/if}
		</button>
	{/each}
</div>

<style>
	.motion-switch {
		display: flex;
		gap: 2px;
		background: var(--surface-2);
		border-radius: 8px;
		padding: 3px;
	}
	.opt {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		height: 30px;
		padding: 0;
		border: none;
		border-radius: 6px;
		background: transparent;
		color: var(--muted);
		cursor: pointer;
		transition: all 0.12s ease;
	}
	.opt:hover {
		color: var(--ink-2);
		background: var(--hover);
	}
	.opt.active {
		background: var(--surface);
		color: var(--s1);
		box-shadow: 0 1px 2px rgba(0, 0, 0, 0.15);
	}
</style>
