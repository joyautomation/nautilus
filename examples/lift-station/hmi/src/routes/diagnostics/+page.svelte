<script lang="ts">
	// Diagnostics: how the station and the controller are doing over time.
	// The level trend (kept by the app's client from the moment it
	// connected, so it already has history when you arrive), the field
	// drivers, and the scan timing of every task.
	import { Trend, DriverStatusPanel, ScanDiagnostics } from '@joyautomation/nautilus-hmi';
	import { rt, levelBuf } from '$lib/client.svelte';
</script>

<svelte:head><title>Lift Station · Diagnostics</title></svelte:head>

<div class="scroll">
	<div class="page">
		<section class="card">
			<h2>Wet-well level · LIT-101</h2>
			<Trend series={[{ name: 'Level', color: 'var(--s1)', points: levelBuf.points }]} unit="%" yMin={0} yMax={100} height={200} />
		</section>

		<section class="card">
			<h2>Scan diagnostics</h2>
			{#if rt.frame}
				<ScanDiagnostics scan={rt.frame.scan} />
			{:else}
				<p class="muted">Waiting for the controller…</p>
			{/if}
		</section>

		<section class="card">
			<h2>Field drivers</h2>
			<DriverStatusPanel drivers={rt.frame?.drivers ?? []} />
		</section>
	</div>
</div>

<style>
	.scroll {
		position: absolute;
		inset: 0;
		overflow-y: auto;
	}
	.page {
		max-width: 1100px;
		margin: 0 auto;
		padding: 20px 24px 48px;
		display: grid;
		gap: 16px;
	}
	.card {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 10px);
		padding: 16px;
	}
	h2 {
		font-size: 0.78rem;
		font-weight: 700;
		letter-spacing: 0.07em;
		text-transform: uppercase;
		color: var(--muted);
		margin: 0 0 0.7rem;
	}
	.muted {
		color: var(--muted);
		margin: 0;
	}
</style>
