import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
export default {
	preprocess: vitePreprocess(),
	kit: {
		// A pure client SPA: it talks to the rig controller's tag API. The
		// fallback makes every route serve index.html, so the built bundle
		// drops in beside the controller via server.hmi in nautilus.yaml.
		adapter: adapter({ fallback: 'index.html' }),
		// Served from somewhere other than "/" (the nautilus playground serves a
		// build of this at its own path): HMI_BASE, e.g. /some/path. Unset, it
		// is "/", as beside the controller.
		paths: { base: process.env.HMI_BASE ?? '' }
	}
};
