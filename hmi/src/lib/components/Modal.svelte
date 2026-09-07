<script lang="ts">
	// Dialog built on the native <dialog> element — showModal() gives us a
	// browser-native focus trap, top-layer stacking, and ::backdrop for free.
	// Open state is bindable so a host can drive it (`bind:open`) or just set
	// it once and let the close affordances (×, Escape, backdrop click) take
	// it from there.
	import type { Snippet } from 'svelte';
	import Icon from './Icon.svelte';

	let {
		open = $bindable(false),
		title = '',
		size = 'md',
		closeOnBackdrop = true,
		closeOnEscape = true,
		onclose,
		header,
		children,
		footer
	}: {
		open?: boolean;
		title?: string;
		size?: 'sm' | 'md' | 'lg' | 'full';
		closeOnBackdrop?: boolean;
		closeOnEscape?: boolean;
		onclose?: () => void;
		/** Overrides the default title-bar header. */
		header?: Snippet;
		children?: Snippet;
		footer?: Snippet;
	} = $props();

	let dialogEl: HTMLDialogElement | null = $state(null);

	$effect(() => {
		if (!dialogEl) return;
		if (open && !dialogEl.open) dialogEl.showModal();
		else if (!open && dialogEl.open) dialogEl.close();
	});

	function requestClose() {
		open = false;
		onclose?.();
	}

	// The dialog element itself fills the ::backdrop area when shown; a click
	// that lands on it (not on .box) is a backdrop click.
	function onBackdropClick(ev: MouseEvent) {
		if (closeOnBackdrop && ev.target === dialogEl) requestClose();
	}

	function oncancel(ev: Event) {
		// `cancel` fires natively on Escape; always prevent the default (which
		// would call .close() directly) so `open` stays the single source of
		// truth and `onclose` still fires.
		ev.preventDefault();
		if (closeOnEscape) requestClose();
	}
</script>

<dialog
	bind:this={dialogEl}
	class="modal {size}"
	{oncancel}
	onclick={onBackdropClick}
	onclose={() => {
		if (open) requestClose();
	}}
>
	<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
	<div class="box" onclick={(e) => e.stopPropagation()}>
		<header>
			{#if header}
				{@render header()}
			{:else}
				<h3>{title}</h3>
			{/if}
			<button type="button" class="x" aria-label="Close" onclick={requestClose}>
				<Icon name="x-mark" size={16} strokeWidth={1.75} />
			</button>
		</header>
		<div class="body">
			{#if children}{@render children()}{/if}
		</div>
		{#if footer}
			<footer>
				{@render footer()}
			</footer>
		{/if}
	</div>
</dialog>

<style>
	.modal {
		padding: 0;
		border: 1px solid var(--border);
		border-radius: 12px;
		background: var(--surface);
		color: var(--ink);
		box-shadow: 0 18px 50px rgba(0, 0, 0, 0.45);
		max-height: min(85vh, 760px);
		width: min(480px, calc(100vw - 32px));
	}
	.modal.sm {
		width: min(360px, calc(100vw - 32px));
	}
	.modal.lg {
		width: min(680px, calc(100vw - 32px));
	}
	.modal.full {
		width: calc(100vw - 32px);
		height: calc(100vh - 32px);
		height: calc(100dvh - 32px);
		max-height: none;
	}
	.modal::backdrop {
		background: color-mix(in srgb, var(--bg) 55%, transparent);
		backdrop-filter: blur(2px);
	}
	.modal[open] {
		display: flex;
		animation: rise 0.14s ease-out;
	}
	@keyframes rise {
		from {
			opacity: 0;
			transform: translateY(8px) scale(0.985);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.modal[open] {
			animation: none;
		}
	}
	.box {
		display: flex;
		flex-direction: column;
		min-height: 0;
		width: 100%;
	}
	header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 10px;
		padding: 14px 16px 10px;
		border-bottom: 1px solid var(--border);
		flex: none;
	}
	header h3 {
		margin: 0;
		font-size: var(--font-md);
		font-weight: 680;
		color: var(--ink);
	}
	.x {
		flex: none;
		width: 28px;
		height: 28px;
		padding: 0;
		display: grid;
		place-items: center;
		border: 1px solid var(--border);
		border-radius: 7px;
		background: transparent;
		color: var(--muted);
		line-height: 1;
		cursor: pointer;
	}
	.x:hover {
		color: var(--ink);
		border-color: var(--muted);
	}
	.x:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.body {
		padding: 14px 16px 16px;
		overflow-y: auto;
		display: grid;
		gap: 14px;
		flex: 1;
		min-height: 0;
	}
	footer {
		flex: none;
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		padding: 12px 16px;
		border-top: 1px solid var(--border);
	}
</style>
