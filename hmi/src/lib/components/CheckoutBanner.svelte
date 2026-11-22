<script lang="ts">
	// A standing I/O checkout force (#719), on every screen while it stands
	// (the checkout page is not the only place a person looks). The time left
	// is the controller's own figure from the last frame, counted down here; a
	// stream gone quiet says the state is unknown, and the timer reaching zero
	// says "ending", never "released": only a frame without the force says
	// there is none. A project with no checkout never has the field, so this
	// shows nothing there.
	import { countdown, forceBanner, type CheckoutForce } from '../checkout.js';

	let { frame, staleMs = 3000 }: { frame: { checkoutForce?: CheckoutForce } | null | undefined; staleMs?: number } = $props();

	let frameAt = $state(0);
	let now = $state(0);
	let seen = $state<CheckoutForce | null>(null);

	// each new frame resyncs the countdown (a frame is a new object)
	$effect(() => {
		if (!frame) return;
		frameAt = now = performance.now();
		if (frame.checkoutForce) seen = frame.checkoutForce;
		else seen = null;
	});
	$effect(() => {
		const id = setInterval(() => (now = performance.now()), 500);
		return () => clearInterval(id);
	});

	const force = $derived(frame?.checkoutForce);
	const cd = $derived(frame ? countdown(force, frameAt, now, staleMs) : ({ state: 'none' } as const));
	// a quiet stream with no force ever seen is just a quiet stream: the
	// connection badge says so, and a project with no checkout shows nothing
	const text = $derived(cd.state === 'unknown' && !seen && !force ? '' : forceBanner(cd, force ?? seen));
</script>

{#if text}
	<div class="ck-banner" class:unknown={cd.state === 'unknown'} role="status" aria-live="polite">{text}</div>
{/if}

<style>
	.ck-banner {
		padding: 6px 16px;
		font-size: var(--font-xs, 0.85rem);
		font-weight: 600;
		background: color-mix(in srgb, var(--warn, #d98c00) 22%, var(--surface, transparent));
		color: var(--ink);
		border-bottom: 1px solid var(--warn, #d98c00);
	}
	.unknown {
		background: color-mix(in srgb, var(--danger, #c33) 18%, var(--surface, transparent));
		border-bottom-color: var(--danger, #c33);
	}
</style>
