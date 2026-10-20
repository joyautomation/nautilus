import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { fileURLToPath } from "node:url";

// Third webview bundle: the 3D scene editor — the hmi-3d renderer, Threlte
// and three.js, which no diagram or P&ID should carry. Built AFTER the fbd
// bundle (which owns emptyOutDir) — see the package build script.
//
// hmi-3d is linked from the repo (file:../../../hmi-3d) and has its own
// node_modules; dedupe makes its imports of svelte, three, Threlte and the
// kit resolve to THIS package's copies, so the bundle holds one runtime of
// each (two svelte runtimes cannot share a component tree, two three.js
// cannot share a scene). inlineDynamicImports: the renderer dynamic-imports
// its glTF and HDRI halves, and an IIFE bundle cannot split.
export default defineConfig({
  plugins: [svelte()],
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
  resolve: {
    dedupe: ["svelte", "three", "@threlte/core", "@threlte/extras", "@joyautomation/nautilus-hmi"],
    alias: {
      "@joyautomation/nautilus-hmi": fileURLToPath(new URL("./node_modules/@joyautomation/nautilus-hmi", import.meta.url)),
    },
  },
  build: {
    outDir: "../media/dist",
    emptyOutDir: false,
    chunkSizeWarningLimit: 2000,
    lib: { entry: "src/scene/main.ts", formats: ["iife"], name: "SceneEditor", fileName: () => "scene-editor.js" },
    rollupOptions: { output: { assetFileNames: "scene-editor.[ext]", inlineDynamicImports: true } },
  },
});
