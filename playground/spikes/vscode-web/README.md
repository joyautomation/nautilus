# Spike: the nautilus extension in VS Code for the web (2026-11-06)

Question: can the playground be real VS Code (or very close) instead of a
custom page, and what does it cost to load on a cheap laptop?

- `stub-extension/`: a web extension that starts the playground engine
  (`../../dist`, copy it to `engine/`) in a nested worker, loads the seal-in
  into an in-memory file system, runs it, and shows the scan count in the
  status bar. It ran unchanged in both hosts below.
- VS Code for the web: `npx @vscode/test-web --quality=stable --host localhost
  --extensionDevelopmentPath=stub-extension` (use `localhost`, not
  `127.0.0.1`: the extension-host iframe origin is `<uuid>.<host>`).
- `monaco-vscode-api/`: a minimal workbench from @codingame/monaco-vscode-api
  39.0.3 parts (workbench, extensions with a worker host, files, theme,
  keybindings, quick access, configuration, explorer, textmate). Put the
  stub in `public/ext/` and `vite build && vite preview`.
- `measure.mjs`: Playwright, cold cache, CDP CPU throttle; time until the
  status bar says the program is running; bytes Brotli-compressed in Node.
  `results.jsonl` is the run. naut.wasm (3.6 MB br) is fetched by a worker
  and not counted there; it is the same in every shell.
