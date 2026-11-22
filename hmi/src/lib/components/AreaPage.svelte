<script lang="ts">
	// One area of the plant model (#714): its equipment, each a link to its
	// asset page with the alarms standing on it, and a link to the area's
	// process scheme when it has one. Props in, hrefs out: the host says where
	// a page lives.
	//
	//   <AreaPage {area} {assets} alarms={alarms} href={(a) => '/assets/' + assetSlug(a.id)} />
	import type { AlarmClient } from '../alarms.svelte.js';
	import { PRIORITY_META } from '../alarms.svelte.js';
	import { areaRows, assetTitle, type AreaDescriptor, type AssetDescriptor } from '../asset.js';

	let {
		area,
		assets,
		alarms = null,
		href
	}: {
		area: AreaDescriptor;
		/** Every asset (the area's are picked by id). */
		assets: AssetDescriptor[];
		alarms?: AlarmClient | null;
		/** Where an asset's page is. */
		href: (asset: AssetDescriptor) => string;
	} = $props();

	const rows = $derived(areaRows(area, assets, alarms?.instances ?? []));
</script>

<article class="area" aria-label={area.label ?? area.id}>
	<header>
		<h1>{area.label ?? area.id}</h1>
		{#if area.scheme}<a class="scheme" href={area.scheme}>Process scheme</a>{/if}
	</header>
	{#if rows.length}
		<table>
			<thead>
				<tr><th>Equipment</th><th>Kind</th>{#if alarms}<th>Alarms</th>{/if}</tr>
			</thead>
			<tbody>
				{#each rows as r (r.asset.id)}
					<tr>
						<td><a href={href(r.asset)}>{assetTitle(r.asset)}</a></td>
						<td class="kind">{r.asset.subtitle?.split(' · ')[0] ?? ''}</td>
						{#if alarms}
							<td>
								{#if r.active}
									<span class="stand" class:unacked={r.unacked > 0} style="--c: {PRIORITY_META[r.worst as keyof typeof PRIORITY_META]?.color ?? 'var(--muted)'}">
										{r.active} standing{r.unacked ? `, ${r.unacked} unacknowledged` : ''} ({PRIORITY_META[r.worst as keyof typeof PRIORITY_META]?.label ?? r.worst})
									</span>
								{:else}
									<span class="none">none</span>
								{/if}
								{#if r.suppressed}<span class="oos">{r.suppressed} out of service</span>{/if}
							</td>
						{/if}
					</tr>
				{/each}
			</tbody>
		</table>
	{:else}
		<p class="none">No equipment in this area has a page.</p>
	{/if}
</article>

<style>
	.area {
		display: grid;
		gap: var(--space-3, 12px);
		max-width: 900px;
		margin: 0 auto;
	}
	header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-3);
	}
	h1 {
		margin: 0;
		font-size: var(--font-lg);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--font-sm, 0.9rem);
	}
	th,
	td {
		text-align: left;
		padding: var(--space-1) var(--space-2);
		border-bottom: 1px solid var(--border);
	}
	a {
		color: var(--ink);
	}
	.kind,
	.none {
		color: var(--muted);
	}
	.stand {
		color: var(--c);
		font-weight: 600;
	}
	.oos {
		margin-left: var(--space-2);
		color: var(--muted);
		font-size: var(--font-xs);
	}
</style>
