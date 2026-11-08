# @joyautomation/nautilus-playground

Nautilus in a browser tab, with nothing installed: the IEC 61131-3 compiler,
the scan loop, the tag API (including the SSE stream), the language server,
the diagram editors' graph/edit and the acceptance runner, compiled to
WebAssembly (`cmd/naut-wasm`) and run in a Web Worker. The docs site serves
it at nautilus.joyautomation.com/playground/: `website/scripts/sync-playground.mjs`
copies `dist/` to `/playground/engine/`, and `docs.yml` builds and deploys
both.

    ./scripts/build.sh          # -> dist/: naut.wasm, wasm_exec.js, worker.js, memfs.js, client.js, sw.js, app/, hmi/
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

## Their copy and ours, and share links

A project opens from a source that is ours and never changes: an example,
a `?project=` URL, a saved project, or a share link. The first edit makes
the visitor's own copy, kept in their browser (IndexedDB, `app/keep.js`)
under the source's key with a fingerprint of the source it started from. A
later visit opens their copy; if the source has changed since, a notice
offers the new version or keeps theirs. "Playground: Discard My Changes"
goes back to the source.

The Share button (or "Playground: Copy Share Link") saves the project as it
stands with `share/`, a small service beside the docs site, and copies a
short link: `/playground/p/<id>`. A saved project never changes; its id is
a hash of it, so the same project saved twice is one link, and an edited
one saved again is a new link. Opening a link gives whoever opens it their
own copy. An example shared unchanged is just `?example=<name>`.

When the project cannot be saved (the service is down, or this is a dev
server), the link carries the project itself instead, in `#project=`: gzip,
then base64url. A project made from one of our examples carries only the
files that differ from it, and the example's fingerprint; opened after the
example has changed, it says the changes may not fit.

`share/` keeps each project as a gzipped JSON file on a volume (no
database), with a size cap, a rate limit, and a way to take one down
(`DELETE` with `SHARE_ADMIN_TOKEN`; it stays down if saved again). See
`share/main.go` and `share/k8s.yml`. To try it all on one machine, after
`scripts/build.sh` and `npm run build` in `website/`:

    go run ./playground/share -data /tmp/shares -site website/dist -addr 127.0.0.1:5181

## The dashboard and the HMI

`naut run` serves the built-in dashboard at `/`, or a built HMI
(`server.hmi`), beside the tag API. In the playground those pages open in
tabs of their own, served by `src/sw.js`, a service worker the app registers
on `view/` alone (so the workbench never passes through it):

- `view/dashboard/?engine=<channel>`: the dashboard, from the engine.
- `view/<name>/?engine=<channel>`: a prebuilt HMI, `dist/hmi/<name>/`. The
  build compiles it with SvelteKit's `paths.base` set to that path
  (`PLAYGROUND_BASE`, default `/playground/engine`, is where the site mounts
  `dist/`). lift-station ships one; its example JSON says `"hmi"`.

Their `/api/...` requests reach the engine over the playground tab's bridge
channel. If that tab is reloaded or closed, a view moves to the newest
playground tab's engine. The app's `window.nautilusPlayground.views` has
the URLs.

`scripts/wasmsize.py <unstripped.wasm>` attributes code bytes to Go packages
(`go tool nm` does not read wasm).
