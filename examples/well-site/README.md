# well-site — one production well, drawn

The first screen in the spatial package's **paper look**
(`docs/design/spatial-hmi.md` §3f): an isometric line drawing of a well
site, live. The ground is cut away so the casing, the water in it and the
submersible pump at the bottom are in view — the part of a well screen a
P&ID cannot show — and the discharge runs through a meter and a control
valve to a reservoir, with a hypochlorite feed on the line. Nothing on the
page has colour until something is wrong; flow is the chevrons moving.

```sh
naut check .     # validate — the scene file included
naut test  .     # 4 acceptance tests, virtual time
naut run   .     # dashboard + tag API on http://0.0.0.0:8080
naut build .     # one deployable controller binary, 3D view included
```

```
nautilus.yaml      5 struct tags (WL101 Well, FIT101 Meter, FCV101 Valve, CL101 ChemFeed,
                   R101 Tank), setpoints, two fault switches, 7 alarm rules, server.hmi
types.st           the UDTs: the contract the scene binds to
control.st         well pump on reservoir level, valve with the pump, dosing with flow,
                   the drawdown limit with hysteresis, a stuck-valve detector
sim.st             the aquifer (static level, drawdown, a lag), the reservoir, the chlorine
well_test.yaml     the acceptance suite
well.scene.json    the scene: `look: paper`, an orthographic camera on the isometric
                   angle, two component kinds, one assembly, four pipe runs
hmi/               the view: a SvelteKit app on the package, one page
hmi/src/lib/       Well.svelte (the cutaway well) and Meter.svelte — kinds the document
                   names (design §3d); the chem feed is a JSON assembly
```

## The view

`hmi/` consumes the package from this checkout (`file:../../../hmi-3d`),
so build the package once first:

```sh
cd ../../hmi-3d && npm install && npm run package && cd -
cd hmi && npm install
npm run dev             # http://localhost:5173, proxying /api to :8080 —
                        # run `naut run .` in the project root first
```

The document asks for the paper look; `?look=lit` or `?look=flat` on the
URL starts in another, and the button in the corner cycles. The three
looks are one document: the same kinds, the same bindings.

To see it served by the controller itself, one origin, the way it ships:

```sh
cd hmi && npm run build      # -> hmi/build (adapter-static)
cd .. && naut run .          # now serves the view at /
```

## Make something happen

From the dashboard (or `POST /api/tags`), flip:

- `InjectPumpFault` — the well pump trips: its node glows in the alarm's
  colour, the chevrons stop, the valve closes, the reservoir drains.
- `InjectDrought` — the static water level sinks; the well draws down past
  `MaxDepth`, the pump is held off and the drawdown alarm holds (with
  hysteresis) while the level recovers.

The well's label reads the flow and the pumping level; the thin ring on
the casing is the static level.
