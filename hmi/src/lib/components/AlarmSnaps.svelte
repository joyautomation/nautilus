<script lang="ts">
	// The pictures kept when one alarm occurrence activated (#724 slice 2):
	// small thumbnails, each a link to the full picture. Asked for lazily, when
	// the row is drawn; it draws nothing (no element at all) when there are no
	// pictures, and reports a refusal to the journal, which says it once.
	import { thumbSplit, type SnapshotClient, type AlarmSnap } from '../alarmsnaps.js';

	let {
		client,
		occurrence,
		name,
		ondenied
	}: {
		client: SnapshotClient;
		occurrence: string;
		name: string;
		ondenied?: () => void;
	} = $props();

	let snaps = $state<AlarmSnap[]>([]);

	$effect(() => {
		let live = true;
		client.list(occurrence).then((r) => {
			if (!live) return;
			if (r.state === 'denied') ondenied?.();
			snaps = r.state === 'ok' ? r.snaps : [];
		});
		return () => {
			live = false;
		};
	});

	const split = $derived(thumbSplit(snaps));
</script>

{#if snaps.length}
	<span class="snaps">
		{#each split.shown as s (s.url)}
			<a href={s.url} target="_blank" rel="noopener noreferrer" title={s.camera}>
				<img src={s.url} loading="lazy" width="48" height="36" alt="Picture when {name} activated{s.camera ? ` (${s.camera})` : ''}" />
			</a>
		{/each}
		{#if split.more}<span class="more" aria-label="{split.more} more pictures">+{split.more}</span>{/if}
	</span>
{/if}

<style>
	.snaps {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		margin-left: var(--space-2, 8px);
		vertical-align: middle;
	}
	img {
		display: block;
		width: 48px;
		height: 36px;
		object-fit: cover;
		border: 1px solid var(--border);
		border-radius: 4px;
		background: var(--surface-2);
	}
	a:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
		border-radius: 4px;
	}
	.more {
		font-size: var(--font-2xs);
		color: var(--muted);
	}
</style>
