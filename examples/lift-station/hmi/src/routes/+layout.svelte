<script lang="ts">
	// App shell: the operator screen is one page (see +page.svelte), so this
	// layout is just the plumbing every page needs — theme tokens, the
	// realtime client's lifecycle, and the one shared confirm dialog every
	// irreversible action (ack, reset) routes through.
	import '@joyautomation/nautilus-hmi/theme.css';
	import '@joyautomation/nautilus-hmi/fonts.css';
	import './app.css';
	import { onMount } from 'svelte';
	import { theme, ConfirmDialog } from '@joyautomation/nautilus-hmi';
	import { rt, alarms } from '$lib/client.svelte';

	let { children } = $props();
	onMount(() => {
		theme.init();
		rt.start();
		void alarms.start();
		return () => {
			rt.stop();
			alarms.stop();
		};
	});
</script>

<!-- Mounted ONCE, app-wide: every `await confirm({…})` anywhere renders here. -->
<ConfirmDialog />

{@render children()}
