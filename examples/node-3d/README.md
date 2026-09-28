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
  `?lid=on`, `?xray`, `?exploded` set the view.

```sh
(cd ../../hmi-3d && npm run package)
cd hmi && npm install && npm run build && npx vite preview --port 8097
node stub-inventory.mjs <capture dir>/node1 > hmi/src/lib/node1.stub.json
```
