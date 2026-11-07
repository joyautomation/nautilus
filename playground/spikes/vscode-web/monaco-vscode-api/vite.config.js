export default {
	build: { target: 'es2022', chunkSizeWarningLimit: 100000 },
	worker: { format: 'es' },
	optimizeDeps: { esbuildOptions: { target: 'es2022' } },
	server: { port: 5200, strictPort: true }
};
