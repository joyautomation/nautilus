import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// Bundle for a VS Code webview: one JS + one CSS file, no external fetches.
export default defineConfig({
  plugins: [svelte()],
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
  build: {
    outDir: "../media/dist",
    emptyOutDir: true,
    lib: { entry: "src/main.ts", formats: ["iife"], name: "FbdFlow", fileName: () => "fbd-flow.js" },
    rollupOptions: { output: { assetFileNames: "fbd-flow.[ext]" } },
  },
});
