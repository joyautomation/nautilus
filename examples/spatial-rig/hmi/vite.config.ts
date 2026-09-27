import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, loadEnv } from 'vite';
import path from 'node:path';

// The controller to talk to. Set CONTROLLER_URL to point the dev server at
// any running nautilus controller (default: the local dashboard port).
//   CONTROLLER_URL=http://localhost:8080 npm run dev
export default defineConfig(({ mode }) => {
	const env = loadEnv(mode, process.cwd(), '');
	const controller = env.CONTROLLER_URL ?? 'http://localhost:8080';
	return {
		plugins: [sveltekit()],
		resolve: {
			// @joyautomation/nautilus-hmi-3d is a file: link to ../../../hmi-3d,
			// and a linked package resolves its own node_modules first. Two
			// copies of svelte cannot share context and two copies of three
			// cannot share `instanceof`, so pin every shared runtime to this
			// app's copy.
			dedupe: ['svelte', 'three', '@threlte/core', '@threlte/extras', '@joyautomation/nautilus-hmi']
		},
		server: {
			// rig.scene.json lives at the nautilus PROJECT root (one level up),
			// next to nautilus.yaml, the way the lift-station's mimic does.
			// Vite otherwise refuses to serve anything outside its own root.
			fs: { allow: [path.resolve(import.meta.dirname, '..'), path.resolve(import.meta.dirname, '../../../hmi-3d')] },
			// One origin for the browser. The controller's write guard checks
			// Origin, so the dev proxy presents the controller's own.
			proxy: {
				'/api': {
					target: controller,
					changeOrigin: true,
					configure(proxy) {
						proxy.on('proxyReq', (proxyReq) => {
							proxyReq.setHeader('origin', controller);
						});
					}
				}
			}
		}
	};
});
