<script lang="ts">
	import '@joyautomation/nautilus-hmi/theme.css';
	let { children } = $props();
</script>

{@render children()}

<style>
	:global(html, body) {
		margin: 0;
		height: 100%;
		background: var(--surface, #ffffff);
		color: var(--ink, #1b1a17);
		font-family: system-ui, sans-serif;
	}
</style>
