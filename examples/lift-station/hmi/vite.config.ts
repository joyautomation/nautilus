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
			// app's copy (the same arrangement as examples/spatial-rig/hmi).
			dedupe: ['svelte', 'three', '@threlte/core', '@threlte/extras', '@joyautomation/nautilus-hmi']
		},
		server: {
			// lift-station.mimic.json and lift-station.scene.json live at the
			// nautilus PROJECT root (one level up from this SvelteKit project)
			// so the VS Code editors and this app share exactly one file each
			// — the extension finds any *.mimic.json / *.scene.json anywhere
			// in the workspace, but Vite's dev server otherwise refuses to
			// read/transform anything outside its own project root. Without
			// this, `npm run dev` 403s on the import the moment it's
			// requested (see the dogfood log). The hmi-3d checkout is the
			// linked package's source.
			fs: { allow: [path.resolve(__dirname, '..'), path.resolve(__dirname, '../../../hmi-3d')] },
			// Proxy /api to the controller so the browser sees one origin.
			// changeOrigin rewrites Host but NOT the Origin header — the
			// controller's same-origin write guard would still see the vite
			// origin and 403 tag writes. The dev proxy genuinely fronts the
			// controller, so present the controller's own origin. (In
			// production this doesn't arise: the built SPA is served from
			// the controller itself, or writes carry a token.)
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
