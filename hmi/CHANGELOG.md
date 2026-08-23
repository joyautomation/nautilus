# Changelog — @joyautomation/nautilus-hmi

## Unreleased — suggest **0.6.0** (minor: additive, no breaking changes)

Everything below came out of the Pomona WRD recreation (`pomona/wrd/host/hmi`), which ported an
Ignition Perspective application onto this kit and kept a running list of what it had to build
locally. These are those items, generalised — the app now imports them and its local copies are
gone. Nothing here changes an existing component's default rendering.

### Added — porting a legacy HMI

- **`CoordinateCanvas`** (+ `./canvas.ts`: `CanvasSpec`, `CanvasNode`, `CanvasRegistry`,
  `CONTAINER_TYPES`, `inlineStyle`, `flexStyle`, `walkSpec`). A fixed coordinate plane scaled to
  the viewport, with a component **registry** (and/or a `leaf` snippet) injected by the host app.
  The single most reusable thing for anyone porting an Ignition, FactoryTalk or WinCC project:
  every legacy screen is a fixed plane of absolutely-placed symbols that must keep its aspect
  ratio. Hooks for live `visible` / `style` / `href` bindings keep the transcriber out of the kit.
- **`EquipSymbol`** — a raster symbol with SCADA state chrome: run tint, simulate/comm-fail
  outline, fault bell, A/M · R/L chips, and `contain`-fit sizing when both `width` and `height`
  are given (legacy symbol PNGs have wildly different aspect ratios; sizing off `width` alone
  renders half of them four times too tall).
- **`ScaleBar`** — the moving analog indicator: a value against a scale cut into coloured alarm
  bands, vertical or horizontal. A disabled limit is `null` rather than a separate enable flag, so
  a UDT's per-limit enable maps straight onto the prop.
- **`StatusRow`** (+ `RowState`) — one device per line: mode chips, name, value snippet, on a
  background carrying `'on' | 'off' | 'fault' | 'unknown'`. `unknown` never collapses into `off`.
- **`LevelTank`** — a level-only vessel: range, band marks, an overflow mark, percentage and
  volume labels. The kit's `Tank` remains the heated-vessel twin (`tempC` / `heaterPct`), unchanged.
- **Schematic glyphs** `TankGlyph`, `PumpGlyph`, `ValveGlyph`, `FlowLink` — bare SVG `<g>`
  primitives for a network view you lay out yourself. `FlowLink` draws its marching dashes only
  when `flowing`, so a still line means still water.

### Added — writing back

- **`WriteNumber`**, **`WriteToggle`**, **`CommandButton`** — a labelled setpoint field, a
  lamp/switch and a momentary pushbutton, all API-agnostic: each takes `write(name, value)`
  resolving to `null` on success or the controller's refusal reason (the shape
  `RealtimeClient.writeTag` already returns). Refusals render on the control; `readonly` +
  `readonlyReason` covers a point the runtime will not accept.

### Added — trends and tags

- **`useTrend(client, tag, opts)`** (`./trend.svelte.ts`) — one tag's trend, live off the SSE
  stream and backfilled from `GET /api/history` on mount and on every stream reopen. Buffers are
  shared and refcounted per (client, tag, window).
- **`fetchHistory`**, **`historyAvailable`**, **`mergeHistory`** (`./history.ts`) — the historian
  query, a `/history/span` probe so "no historian configured" is reported as data rather than as a
  failed query, and the splice that lets the live buffer win over bucket-averaged history.
- **`tagAt`**, **`numAt`**, **`boolAt`**, **`hasTagAt`**, **`numericLeaves`** (`./tags.ts`) —
  dotted struct-path resolution against a frame's tag map, and an enumeration of every finite
  number a runtime is currently publishing. Build a tag picker from `numericLeaves` and it can
  never offer a tag that resolves to nothing.

### Changed

- **`Gauge`** gained `sweep` (degrees of scale, default 240 — unchanged) and `gap` (its
  complement, for the 360° donut idiom), plus `thickness`. The drawing box grows downward as the
  arc closes and the end labels move off the seam past 300°. **The default rendering is byte-for-byte
  what it was**: `sweep = 240` still yields the same 170×130 viewBox.
- **`RealtimeClient.onOpen`** now accepts multiple subscribers and returns an unsubscribe
  function (it previously kept a single callback, which `useTrend` would have clobbered). Existing
  single-callback callers are unaffected.
- **`theme.css`** defines `.blinking` (an unscoped attention-flash rule, with a reduced-motion
  outline fallback) and the legacy-port token families documented in the README's Theming section.

### Docs

- README: a **Porting a legacy HMI** section, a **Writing back** section, a
  **Trends over history** section, the legacy-port token families under
  Theming, and every new component in the props table.
- `examples/hmi-demo` gained a **`/legacy`** route exercising all of the
  above against a synthetic sine wave — it needs no controller running.

### Fixed

- `TankGlyph` takes its liquid colour from the `fill` **attribute** and no longer sets `fill` on
  it from CSS. In the app this glyph came from, a `.glyph rect { fill: … }` rule was silently
  overriding the band colour every caller passed, so the level bar rendered in the surface colour.
