import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// SvelteKit is only used here as the scaffolding svelte-package builds on; this
// library ships no app of its own.
export default defineConfig({
	plugins: [sveltekit()]
});
