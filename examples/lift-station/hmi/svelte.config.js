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
		adapter: adapter({ fallback: 'index.html' }),
		// The kit is a file: link to ../../../hmi (until its next release is
		// on npm), and the linked hmi-3d brings its own installed copy. One
		// alias makes both — and the type checker — see the same kit, so its
		// classes (AlarmClient, RealtimeClient) are one type, not two.
		alias: { '@joyautomation/nautilus-hmi': '../../../hmi/dist' }
	}
};
