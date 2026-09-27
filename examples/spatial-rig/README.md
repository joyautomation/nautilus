# spatial-rig — the office rig, in 3D

The test bed for the spatial HMI: three physical props on a desk (a tank,
a pump, a valve) given a live, simulated process by this controller, and a
**3D operator view** of them built on
[`@joyautomation/nautilus-hmi-3d`](../../hmi-3d). Everything about the
view is in one file, `rig.scene.json`: where each prop is, which struct tag
it binds, where the pipes run. The page that shows it is fifteen lines.

```sh
naut check .     # validate
naut test  .     # 4 acceptance tests, virtual time
naut run   .     # dashboard + tag API on http://0.0.0.0:8080
naut build .     # one deployable controller binary, 3D view included
```

```
nautilus.yaml    3 struct tags (T101 Tank, P101 Motor, XV101 Valve), setpoints,
                 fault-injection switches, 4 alarm rules, server.hmi: hmi/build
types.st         the Tank / Motor / Valve UDTs: the contract a scene binds to
control.st       pump seal-in on level, valve follows demand, limit bits
sim.st           the process, plus fault injection
rig_test.yaml    the acceptance suite
rig.scene.json   the scene: 3 nodes bound to the struct tags, 2 pipes, a desk, a camera
assets.yaml      marker id → asset → tag, and surveyed positions (for AR, later)
hmi/             the 3D view: a SvelteKit app on the package, one page
```

## The 3D view

`hmi/` consumes the package from this checkout (`file:../../../hmi-3d`), so
build the package once first:

```sh
cd ../../hmi-3d && npm install && npm run package && cd -
cd hmi && npm install
npm run dev             # http://localhost:5173, proxying /api to :8080 —
                        # run `naut run .` in the project root first
```

To see it served by the controller itself, one origin, the way it ships:

```sh
cd hmi && npm run build      # -> hmi/build (adapter-static)
cd .. && naut run .           # server.hmi: hmi/build serves it at "/"
```

The controller caches `_app/immutable/*` for a year, so after a rebuild
hard-refresh (or open `/?v=2`) or you will be looking at the old bundle.

What you see: the tank fills and drains with `T101.Level`, the pump's
coupling turns at `P101.Speed` and the pump goes green while running, the
valve handle follows `XV101.Pos`, the pipes show flow. Orbit with the
mouse; click an asset for its 2D faceplate, every member of its struct,
its quality and its alarms. The HUD shows the frame rate and the p95 of
tag-change → pixel (the controller's frame timestamp to the animation
frame after the one that drew it), which is what the package's
performance budget is judged on.

### Make something happen

The process cycles on its own about every two minutes (the pump fills
T-101 from 35 % to 75 %, the drain takes it back). From the dashboard at
`/_nautilus/` (or `POST /api/tags`):

| Want | Do |
|---|---|
| pump trip + an amber halo on P-101 | set `InjectPumpFault` true |
| valve alarm on XV-101 | set `InjectValveStick` true, then change `Demand` by > 10 % |
| tank high-high | set `StopLevel` to 95 |
| tank low-low | set `InjectPumpFault` true and wait ~5 min |
| stale: every model greys out | stop `naut run` |

A halo pulses while the alarm is unacknowledged and holds steady once
acked (from the dashboard's alarm list).

### Nobody types a scene

`rig.scene.json` was written by hand for the spike, but a new project
starts from the manifest instead:

```sh
naut scene init .          # one node per struct tag a kind can draw, on a grid
naut check .               # holds every *.scene.json to the tags and UDTs
```

`init` names the struct tags it could not place and the `--kind Type=kind`
that would; `check` reports unknown kinds, a tag whose UDT is not the kind's
type, a member the struct does not have, and a malformed ref, each with a
JSON path. In VS Code the extension's schema gives completion and the same
structural checks as you type. Design: `docs/design/spatial-hmi.md` §3b.

### How the scene binds

Each prop is **one struct tag**, so a node binds `P101` and the pump model
reads `P101.Running`, `P101.Speed` and `P101.Fault` off it — the same shape
a Sparkplug Template or a Logix UDT arrives in. `bind` adds explicit props
in the mimic's grammar with dotted paths: the valve's `"cmd": "Demand"`
reads the operator setpoint, and a pipe's `"flowing": "P101.Running"`
animates it. See the package README for the document format.

## The physical side

Any objects the right size will do; nothing is plumbed or powered. Spread
them over 2–3 m at different heights so AR tracking gets tested at more
than one distance. Once placed, measure each marker centre from the origin
marker and fill in `pos` in `assets.yaml`; that survey is the ground truth
the AR phases score drift against.

| Prop | Stand-in | Marker |
|---|---|---|
| origin | a marker taped to the desk corner, never moved | 0 |
| T-101 | a clear bucket or small tote, on the floor | 1 |
| P-101 | any small pump or motor, on the desk | 2 |
| XV-101 | a ball valve on a pipe stub, at head height | 3 |

Brief and roadmap: [`docs/design/spatial-hmi.md`](../../docs/design/spatial-hmi.md).
