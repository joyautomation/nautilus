import { defineConfig } from "vite";

// The diagram exporter (src/export): not a webview bundle but a CommonJS
// module the EXTENSION HOST requires, so "Export Diagram…" draws with the
// very layout code the LD/FBD/SFC editors use. Pure TypeScript — no Svelte,
// no DOM — and built AFTER the fbd bundle (which owns emptyOutDir).
export default defineConfig({
  build: {
    outDir: "../media/dist",
    emptyOutDir: false,
    minify: false,
    target: "node18",
    lib: { entry: "src/export/index.ts", formats: ["cjs"], fileName: () => "diagram-export.cjs" },
  },
});
