import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, loadEnv } from 'vite';

// The controller to talk to. `npm run dev` and `npm run preview` proxy /api
// to it, so the page is same-origin exactly as it is when naut serves the
// build from server.hmi:
//   CONTROLLER_URL=http://<epic>:8086 npm run dev
export default defineConfig(({ mode }) => {
	const env = loadEnv(mode, process.cwd(), '');
	const controller = env.CONTROLLER_URL ?? 'http://localhost:8086';
	const proxy = {
		'/api': {
			target: controller,
			changeOrigin: true
		}
	};
	return {
		plugins: [sveltekit()],
		resolve: {
			// One copy of each shared runtime: two sveltes cannot share
			// context and two threes cannot share `instanceof`.
			dedupe: ['svelte', 'three', '@threlte/core', '@threlte/extras', '@joyautomation/nautilus-hmi']
		},
		server: { proxy },
		preview: { proxy }
	};
});
