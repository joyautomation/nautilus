import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// SvelteKit is only the scaffolding svelte-package builds on; this library
// ships no app of its own (same arrangement as hmi/).
export default defineConfig({
	plugins: [sveltekit()]
});
