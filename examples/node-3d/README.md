# node-3d — node1, opened up

The Supermicro SYS-112B-WR in the office cluster, drawn from its chassis
profile (`hmi-3d/profiles/supermicro-sys-112b-wr.json`) crossed with its
tags (design doc §3e). Click any part for its faceplate; lid, x-ray,
exploded and camera presets in the toolbar.

- Fans, PSUs, temperatures and the chassis come from a controller with
  NODE1's it-drivers tags: the phone-AR bench on `:8080` (the recorded X14),
  or `CONTROLLER_URL=http://localhost:8081` for the live rack.
- Drives, DIMMs, cards and the CPU are **stubbed** until the drivers publish
  them (workstream A): `stub-inventory.mjs` turns a Redfish capture into
  `hmi/src/lib/node1.stub.json` (serials redacted), laid under the
  controller's frame by `hmi/src/lib/stub.ts`. A real tag always wins.
- `?pull=NODE1_Drive_NVMe2` shows a pulled drive; `?view=top|front|rear`,
  `?lid=on`, `?xray`, `?exploded`, `?codes` (the printed AR codes, off by default) and `?overlay=heat|interfaces|free` set the view.

- `?overlay=cables` labels every port with its far end from the site
  topology (`hmi/src/lib/hq.topology.json`) and checks each declared link
  live: ✓ confirmed (LLDP / MAC), = consistent (both up, same speed),
  ✗ contradicted, ↓ down, ? unverified.
- `/rack`: the whole cluster racked — three nodes and three S3900 switches
  (`hmi-3d/profiles/fs-s3900-24t4s-r.json`) placed by `hmi/src/lib/hq.rack.json`
  (units and faces **assumed** until measured), every cable drawn between its
  exact ports and coloured by its check. Click a device to zoom in (lid,
  x-ray, exploded, overlays), a part for its faceplate; `?focus=NODE2`,
  `?view=cables|rear|front`. The switches are live only where the controller
  polls them (hq-sw1 on `:8081` today): serve with
  `CONTROLLER_URL=http://localhost:8081`.
- **Simulation mode**: the whole cluster with no hardware —
  `randd/node-3d-sim/sim` replays the recorded switches and BMCs through
  `naut snmp serve` / `naut redfish serve` into a controller on `:8085`;
  serve with `CONTROLLER_URL=http://localhost:8085`.
- `/rack?mesh` (or **mesh** in the toolbar): the same devices and links as a
  network floating in space — switches on a ring, each server under the two
  switches it is cabled to, every link an arc labelled with its two ports and
  coloured by the same check. Click a device to go to it in the rack.

```sh
(cd ../../hmi-3d && npm run package)
cd hmi && npm install && npm run build && npx vite preview --port 8097
node stub-inventory.mjs <capture dir>/node1 > hmi/src/lib/node1.stub.json
```
