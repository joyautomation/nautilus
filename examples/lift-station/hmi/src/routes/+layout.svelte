<script lang="ts">
	// App shell: a bar across the top (the station, the view switch between
	// the P&ID, 3D and diagnostics pages, the alarm banner and ack, the
	// connection) over a page that fills the rest of the window. The alarm
	// banner opens the alarms panel (AlarmsPanel) over any of them. Also the
	// plumbing every page needs: theme tokens, the realtime client's
	// lifecycle, and the one shared confirm dialog every irreversible action
	// (ack, reset) routes through.
	import '@joyautomation/nautilus-hmi/theme.css';
	import '@joyautomation/nautilus-hmi/fonts.css';
	import './app.css';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { theme, ConfirmDialog, AlarmBanner, AckButton, StatusPill, ThemeSwitch } from '@joyautomation/nautilus-hmi';
	import AlarmsPanel from '$lib/AlarmsPanel.svelte';
	import { rt, alarms } from '$lib/client.svelte';

	let { children } = $props();
	// The alarm banner opens the alarms panel, over whichever view is up.
	let alarmsOpen = $state(false);
	onMount(() => {
		theme.init();
		rt.start();
		void alarms.start();
		return () => {
			rt.stop();
			alarms.stop();
		};
	});

	// The query string rides along (the nautilus playground names its
	// engine in it), so a reload on any page still finds the controller.
	const VIEWS = [
		{ id: '/', href: resolve('/'), label: 'P&ID' },
		{ id: '/3d', href: resolve('/3d'), label: '3D' },
		{ id: '/diagnostics', href: resolve('/diagnostics'), label: 'Diagnostics' }
	];
</script>

<!-- Mounted ONCE, app-wide: every `await confirm({…})` anywhere renders here. -->
<ConfirmDialog />

<div class="app">
	<header class="bar">
		<div class="title">
			<b>Lift Station</b>
			<span>WW-101 duplex sewage lift station, P-101 / P-102 duty/standby</span>
		</div>
		<nav class="views" aria-label="Views">
			{#each VIEWS as v (v.id)}
				<a href={v.href + page.url.search} aria-current={page.route.id === v.id ? 'page' : undefined}>{v.label}</a>
			{/each}
		</nav>
		<div class="status">
			<div class="alarm"><AlarmBanner summary={alarms.summary} compact onclick={() => (alarmsOpen = !alarmsOpen)} /></div>
			<AckButton alarms={alarms.active} ids={['*']} onack={(ids, by) => alarms.ack(ids, by)} />
			<StatusPill kind={rt.connected ? 'good' : 'critical'} label={rt.connected ? 'Connected' : 'Offline'} />
			<ThemeSwitch />
		</div>
	</header>
	<main class="view">
		{@render children()}
		<AlarmsPanel bind:open={alarmsOpen} />
	</main>
</div>

<style>
	.app {
		height: 100vh;
		height: 100dvh;
		display: flex;
		flex-direction: column;
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 12px 20px;
		flex-wrap: wrap;
		padding: 10px 16px;
		border-bottom: 1px solid var(--border);
		background: var(--surface);
	}
	.title {
		display: flex;
		align-items: baseline;
		gap: 10px;
		min-width: 0;
	}
	.title b {
		font-size: 1.05rem;
		font-weight: 680;
		white-space: nowrap;
	}
	.title span {
		font-size: 0.8rem;
		color: var(--muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.views {
		display: flex;
		gap: 2px;
		padding: 2px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--surface-2);
	}
	.views a {
		padding: 4px 12px;
		border-radius: 6px;
		font-size: 0.8rem;
		font-weight: 600;
		color: var(--ink-2);
		text-decoration: none;
	}
	.views a:hover {
		color: var(--ink);
	}
	.views a[aria-current='page'] {
		background: var(--s1);
		color: var(--bg);
	}
	.status {
		display: flex;
		align-items: center;
		gap: 10px;
		margin-left: auto;
	}
	/* The banner fills whatever it is given; here, a slot of its own. */
	.alarm {
		width: clamp(150px, 20vw, 260px);
	}
	.view {
		flex: 1;
		min-height: 0;
		position: relative;
		/* Pages scroll inside themselves; a panel sliding in from off the
		   edge must not scroll the window sideways. */
		overflow: hidden;
	}
	@media (max-width: 900px) {
		.title span {
			display: none;
		}
	}
</style>
