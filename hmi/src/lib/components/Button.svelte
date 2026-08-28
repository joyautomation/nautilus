<script lang="ts">
	// App-shell button: same shape as the kit's global `.btn` (theme.css) with
	// variants, sizes, and optional icon — for when you want a component
	// instead of hand-rolling `class="btn primary"`.
	import type { Snippet } from 'svelte';
	import Icon from './Icon.svelte';

	let {
		variant = 'secondary',
		size = 'md',
		icon,
		iconPosition = 'left',
		disabled = false,
		loading = false,
		type = 'button',
		children,
		onclick,
		...rest
	}: {
		variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
		size?: 'sm' | 'md';
		/** Icon name from ../icons.ts — set with no `children` for an icon-only button (add `aria-label`). */
		icon?: string;
		iconPosition?: 'left' | 'right';
		disabled?: boolean;
		loading?: boolean;
		type?: 'button' | 'submit' | 'reset';
		children?: Snippet;
		onclick?: (ev: MouseEvent) => void;
		[key: string]: unknown;
	} = $props();

	const iconOnly = $derived(!!icon && !children);
</script>

<button
	{type}
	class="nhbtn {variant} {size}"
	class:icon-only={iconOnly}
	disabled={disabled || loading}
	aria-busy={loading}
	{onclick}
	{...rest}
>
	{#if loading}
		<span class="spinner" aria-hidden="true"></span>
	{:else if icon && iconPosition === 'left'}
		<Icon name={icon} size={size === 'sm' ? 14 : 16} />
	{/if}
	{#if children}<span class="lbl">{@render children()}</span>{/if}
	{#if !loading && icon && iconPosition === 'right'}
		<Icon name={icon} size={size === 'sm' ? 14 : 16} />
	{/if}
</button>

<style>
	.nhbtn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 6px;
		border-radius: 8px;
		font: inherit;
		font-weight: 550;
		cursor: pointer;
		white-space: nowrap;
		border: 1px solid var(--border);
		background: var(--surface-2);
		color: var(--ink);
		transition: background 0.12s;
	}
	.nhbtn.md {
		padding: 7px 14px;
		font-size: var(--font-xs);
	}
	.nhbtn.sm {
		padding: 4px 10px;
		font-size: var(--font-xs);
	}
	.nhbtn.icon-only.md {
		padding: 7px;
	}
	.nhbtn.icon-only.sm {
		padding: 5px;
	}
	.nhbtn:hover:not(:disabled) {
		background: var(--hover);
	}
	.nhbtn:disabled {
		opacity: 0.45;
		cursor: default;
	}
	.nhbtn:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.nhbtn.primary {
		background: var(--primary-bg);
		border-color: var(--primary-border);
		color: #fff;
	}
	.nhbtn.primary:hover:not(:disabled) {
		background: var(--primary-hover);
	}
	.nhbtn.ghost {
		background: transparent;
		border-color: transparent;
		color: var(--ink-2);
	}
	.nhbtn.ghost:hover:not(:disabled) {
		background: var(--hover);
		color: var(--ink);
	}
	.nhbtn.danger {
		background: transparent;
		border-color: var(--crit);
		color: var(--crit);
	}
	.nhbtn.danger:hover:not(:disabled) {
		background: color-mix(in srgb, var(--crit) 12%, transparent);
	}
	.spinner {
		width: 13px;
		height: 13px;
		border-radius: 50%;
		border: 2px solid currentColor;
		border-top-color: transparent;
		opacity: 0.7;
		animation: spin 0.7s linear infinite;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.spinner {
			animation-duration: 1.4s;
		}
	}

	/* Coarse pointer (touch/pen): grow to the minimum hit area. Desktop
	   density (mouse/trackpad) is untouched. */
	@media (pointer: coarse) {
		.nhbtn.md,
		.nhbtn.sm {
			min-height: var(--tap);
		}
		.nhbtn.icon-only.md,
		.nhbtn.icon-only.sm {
			min-width: var(--tap);
		}
	}
</style>
