<script lang="ts">
	import '@joyautomation/nautilus-hmi/theme.css';
	let { children } = $props();
</script>

{@render children()}

<style>
	:global(html, body) {
		margin: 0;
		height: 100%;
		background: var(--surface, #161615);
		color: var(--ink, #e8e6e1);
		font-family: system-ui, sans-serif;
	}
</style>
