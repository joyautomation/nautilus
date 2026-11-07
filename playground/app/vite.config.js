// Builds the playground app into ../dist/app (scripts/build.sh). Relative
// asset URLs, so the app works wherever the package is served from.
import { fileURLToPath } from 'node:url';

export default {
	base: './',
	resolve: {
		alias: { '@xterm/addon-ligatures': fileURLToPath(new URL('./xterm-ligatures-stub.js', import.meta.url)) }
	},
	build: { outDir: '../dist/app', emptyOutDir: true, target: 'es2022', chunkSizeWarningLimit: 100000 },
	worker: { format: 'es' },
	optimizeDeps: { esbuildOptions: { target: 'es2022' } },
	server: { port: 5300, strictPort: true, fs: { allow: ['..'] } }
};
