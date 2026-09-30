import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, loadEnv } from 'vite';
import path from 'node:path';

// The controller with NODE1's tags. The phone-AR bench (the recorded X14
// replayed by `naut redfish serve`) is on :8080; the live rack on :8081.
//   CONTROLLER_URL=http://localhost:8081 npm run dev
// A simulated cluster adds its plant (examples/it-cluster in nautilus), whose
// replay clock and fault inputs the page writes under /plant/api:
//   CONTROLLER_URL=http://localhost:8085 PLANT_URL=http://localhost:8087 npm run dev
type ProxyReq = { setHeader(k: string, v: string): void };
const to = (target: string, rewrite?: (p: string) => string) => ({
	target,
	changeOrigin: true,
	rewrite,
	configure(p: { on(ev: 'proxyReq', fn: (req: ProxyReq) => void): void }) {
		p.on('proxyReq', (req) => req.setHeader('origin', target));
	}
});
export default defineConfig(({ mode }) => {
	const env = loadEnv(mode, process.cwd(), '');
	const controller = env.CONTROLLER_URL ?? 'http://localhost:8080';
	const proxy: Record<string, ReturnType<typeof to>> = { '/api': to(controller) };
	if (env.PLANT_URL) proxy['/plant/api'] = to(env.PLANT_URL, (p) => p.replace(/^\/plant/, ''));
	return {
		plugins: [sveltekit()],
		resolve: {
			// The package is a file: link; pin the shared runtimes to one copy
			// (two Sveltes cannot share context, two threes cannot share instanceof).
			dedupe: ['svelte', 'three', '@threlte/core', '@threlte/extras', '@joyautomation/nautilus-hmi']
		},
		server: {
			fs: { allow: [path.resolve(import.meta.dirname, '../../../hmi-3d')] },
			proxy
		},
		// Served on the tailnet by `tailscale serve` (mira1…ts.net).
		preview: { proxy, allowedHosts: ['.ts.net'] }
	};
});
