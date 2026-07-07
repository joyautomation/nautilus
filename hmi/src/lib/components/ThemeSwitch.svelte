<script lang="ts">
	// Three-way theme selector (System / Light / Dark).
	import { theme, type Theme } from '../theme.svelte.js';

	const options: { value: Theme; label: string; icon: 'computer' | 'sun' | 'moon' }[] = [
		{ value: 'system', label: 'System', icon: 'computer' },
		{ value: 'light', label: 'Light', icon: 'sun' },
		{ value: 'dark', label: 'Dark', icon: 'moon' }
	];
</script>

<div class="theme-switch" role="group" aria-label="Theme">
	{#each options as o}
		<button
			type="button"
			class="opt"
			class:active={theme.value === o.value}
			onclick={() => theme.set(o.value)}
			title={o.label}
			aria-label={o.label}
			aria-pressed={theme.value === o.value}
		>
			{#if o.icon === 'computer'}
				<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<rect x="2" y="3" width="20" height="14" rx="2" /><line x1="8" y1="21" x2="16" y2="21" /><line x1="12" y1="17" x2="12" y2="21" />
				</svg>
			{:else if o.icon === 'sun'}
				<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<circle cx="12" cy="12" r="4" /><path d="M12 2v2" /><path d="M12 20v2" /><path d="m4.93 4.93 1.41 1.41" /><path d="m17.66 17.66 1.41 1.41" /><path d="M2 12h2" /><path d="M20 12h2" /><path d="m6.34 17.66-1.41 1.41" /><path d="m19.07 4.93-1.41 1.41" />
				</svg>
			{:else}
				<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z" />
				</svg>
			{/if}
		</button>
	{/each}
</div>

<style>
	.theme-switch {
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
