# @joyautomation/nautilus-playground

Nautilus in a browser tab, with nothing installed: the IEC 61131-3 compiler,
the scan loop, the tag API (including the SSE stream), the language server,
the diagram editors' graph/edit and the acceptance runner, compiled to
WebAssembly (`cmd/naut-wasm`) and run in a Web Worker. joyautomation.com's
playground is built on it.

    ./scripts/build.sh          # -> dist/: naut.wasm, wasm_exec.js, worker.js, memfs.js, client.js
    python3 -m http.server -d .. 8765   # then open /playground/dev/test.html

## What is different from `naut run`

`GOOS=js` swaps `internal/project/project_full.go` for `project_js.go`:

- Field drivers (modbus, eip, opcua-client, sparkplug-host, snmp, ...) are
  **simulated**: a loopback that is always online, whose inputs read their
  `init:` until written. Nothing is refused; `load()` returns `notices`.
- The OPC UA server and Sparkplug B edge node are off (nothing can connect
  to a tab). Also noticed, not refused.
- No retained memory, no redundancy. The live-scan write gate is wired as on
  a standalone controller.

The project lives in an in-memory file system (`src/memfs.js`) that Go's
`os` calls reach through `globalThis.fs`, so the language server and the
loader read it as if it were on disk, under `/project`.

`scripts/wasmsize.py <unstripped.wasm>` attributes code bytes to Go packages
(`go tool nm` does not read wasm).
