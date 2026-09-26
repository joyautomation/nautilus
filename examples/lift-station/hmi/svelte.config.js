import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
export default {
	preprocess: vitePreprocess(),
	kit: {
		// A pure client SPA: it talks to a nautilus controller's tag API over
		// the network. `fallback` makes every route serve index.html, so the
		// built bundle drops into any static host (or beside the controller
		// via server.hmi in nautilus.yaml — see the README).
		adapter: adapter({ fallback: 'index.html' })
	}
};
