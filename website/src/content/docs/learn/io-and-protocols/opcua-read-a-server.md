---
title: Read a plant OPC UA server
description: "Browse another system's OPC UA server, generate the bindings, read its values into your controller, and command one of them back."
sidebar:
  order: 5
---

Most plants already have an OPC UA server: a PLC's embedded one, a SCADA
gateway, a Kepware. The usual first job for a new controller is to read it.
In this lesson your controller becomes an OPC UA **client** of such a server:
you browse what it offers, generate the bindings, read a level and command a
pump back. About fifteen minutes. You need `naut`
([install](/learn/getting-started/install/)). Make a project for it and stay in
its folder for the rest of the lesson:

```sh
naut new tankmon --template minimal
cd tankmon
```

## A server to practise on

If you have no plant server to hand, this twenty-line stand-in plays one. It
serves a folder `Tank1` with a `Level` that rises and a `PumpRun` you may write.
It uses Python and the `asyncua` library (`pip install asyncua`); save it as
`gateway.py` in a folder of its own outside the project (the stand-in is not part
of your controller), and leave it running:

```python
import asyncio
from asyncua import Server, ua

async def main():
    server = Server()
    await server.init()
    server.set_endpoint("opc.tcp://127.0.0.1:14851/gateway")
    server.set_security_policy([ua.SecurityPolicyType.NoSecurity])
    idx = await server.register_namespace("urn:example:plant")
    tank = await server.nodes.objects.add_folder(idx, "Tank1")
    level = await tank.add_variable(idx, "Level", 40.0, ua.VariantType.Double)
    pump = await tank.add_variable(idx, "PumpRun", False, ua.VariantType.Boolean)
    await pump.set_writable()
    async with server:
        v = 40.0
        while True:
            v = v + 0.5 if v < 95 else 40.0
            await level.write_value(float(v))
            await asyncio.sleep(1)

asyncio.run(main())
```

It offers no security at all, which is right for a lesson on your own computer
and wrong for a plant: see the end.

## 1. Look at what it offers

```sh
naut opcua browse --endpoint opc.tcp://127.0.0.1:14851/gateway --security None --mode None
```

```text
connected to opc.tcp://127.0.0.1:14851/gateway (None)
browsing from i=85...
namespaces:
  ns=0  http://opcfoundation.org/UA/
  ns=1  urn:freeopcua:python:server
  ns=2  urn:example:plant
PATH                NODE ID   TYPE     ACCESS  DESCRIPTION
Aliases/LastChange  i=32852   UInt32   r       
Tank1/Level         ns=2;i=2  Double   r       Level
Tank1/PumpRun       ns=2;i=3  Boolean  rw      PumpRun
3 variables under 9 nodes
```

`browse` only reads: it never writes to the server. Each variable has a
**node id** (how you name it to the server), a type, and whether you may write
it (`rw`). A real server's list is much longer; the command is how you find the
part you want. `--security None --mode None` is needed here only because the
stand-in offers nothing else; by default `naut` asks for the strongest the
server offers.

## 2. Generate the bindings

In the project folder, import just the `Tank1` folder (`--root` is where to start
browsing), name the tags with a prefix, and say which one the controller may
write:

```sh
naut opcua import --endpoint opc.tcp://127.0.0.1:14851/gateway --security None --mode None \
  --root 'ns=2;i=1' --prefix GW_ --writable 'GW_PumpRun'
```

```text
found 2 variables under 3 nodes
wrote opcua_manifest.yaml (2 tag bindings) and tags/opcua.yaml — compose it with `tag-files: [tags/opcua.yaml]`
online tag: opcua_client__Online (no opcua-client driver in nautilus.yaml; the default driver name)
```

Two generated files, never edited by hand. `tags/opcua.yaml` declares
`GW_Level` and `GW_PumpRun` and one more, `opcua_client__Online`: true while the
session to the server is up. `opcua_manifest.yaml` is how to read each node, and
writing is opt-in per tag (`writable: true` is only on `GW_PumpRun`).

## 3. Point the project at the server

In `nautilus.yaml`, replace the scaffold's `tags:` entries (the sample `Sensor`,
`Setpoint` and `Actuator`) with one tag for the operator, and replace the
`driver:` section:

```yaml
tags:
  - { name: PumpStart, role: setpoint, type: BOOL, init: false, desc: Operator start for the pump }

tag-files: [tags/opcua.yaml]

driver:
  type: opcua-client
  endpoint: opc.tcp://127.0.0.1:14851/gateway
  security: None
  security-mode: None
  manifest: opcua_manifest.yaml
  scan-rate: 1s
```

`PumpStart` is a `setpoint`: a value an operator can write, `false` until they
do. (`tag-files` brings in the tags the import generated.) Replace `program.st`, and delete the scaffold's test file `tankmon_test.yaml`
(it tests the old program):

```st
PROGRAM Tank
VAR_EXTERNAL
    GW_Level             : REAL;
    GW_PumpRun           : BOOL;
    opcua_client__Online : BOOL;
END_VAR
GW_PumpRun := PumpStart AND opcua_client__Online AND GW_Level > 45.0;
END_PROGRAM
```

The pump runs only when an operator has asked for it, the server is answering,
and the level is above 45. The `VAR_EXTERNAL` block is needed here: an imported
tag has a role but no type of its own, so the program names the tags it uses
and their types (`Double` on the server is `REAL` here). `naut check` says
`0 with errors`, with no server needed: it checks the manifest offline.

## 4. Run it, and check from the other side

```sh
naut run
```

The dashboard is at <http://localhost:8080> (if that port is already in use,
`naut run` says so and prints the address it moved to; use that one in the
commands below).

```sh
curl -s localhost:8080/api/state | jq -c .tags
```

(`jq` only tidies the output; without it, plain `curl` prints the whole state.)

```text
{"GW_Level":48.5,"GW_PumpRun":false,"PumpStart":false,"opcua_client__Online":true}
```

The level moves, so yours will differ. The controller is reading the server,
and `GW_PumpRun` is false because nobody has asked for the pump. Ask for it:

```sh
curl -s -X POST localhost:8080/api/tags -d '{"name":"PumpStart","value":true}'
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"GW_Level":50.5,"GW_PumpRun":true,"PumpStart":true,"opcua_client__Online":true}
```

That is what your controller says. Ask the server too, with its own client, so
you know the command really arrived. Save this as `ask.py` beside `gateway.py` and run it
(`asyncua` may print a note about a session timeout; ignore it):

```python
import asyncio
from asyncua import Client

async def main():
    async with Client("opc.tcp://127.0.0.1:14851/gateway") as c:
        print("server side: Level =", await c.get_node("ns=2;i=2").read_value(),
              " PumpRun =", await c.get_node("ns=2;i=3").read_value())

asyncio.run(main())
```

```text
server side: Level = 51.0  PumpRun = True
```

`PumpRun` on the server is `True`: your controller wrote it. (Before you asked
for the pump, the same script printed `PumpRun = False`.)

## 5. When the server goes away

Stop the stand-in, and read the tags again after a few seconds:

```text
{"GW_Level":50.5,"GW_PumpRun":false,"PumpStart":true,"opcua_client__Online":false}
```

`opcua_client__Online` is false, and `GW_PumpRun` followed it down because the
program asked for it. The level keeps its last value (so a program that ignores
`__Online` would act on a stale number), and the dashboard's driver card says
`error`, `Connect failed — retrying`. Start the server again: the controller
retries after 1, 2, 4, 8 seconds and reconnects by itself, with no restart,
within about twenty seconds. A few seconds after `opcua_client__Online` is true
again it sends the pump command again, so read once more:

```text
{"GW_Level":49.5,"GW_PumpRun":true,"PumpStart":true,"opcua_client__Online":true}
```

(The server restarted with `PumpRun` back at `False`; the controller wrote
`True` again after it reconnected, so asking again is not needed.)

## Before a real plant

A plant server will not accept `None`. It will want an encrypted session
(`Basic256Sha256` and `SignAndEncrypt`) with a certificate your controller
presents and the **server** trusts, and often a user name (the password goes in
`NAUTILUS_OPCUA_PASSWORD`, never in a file). The first secured connect is
refused until someone approves the controller's certificate on the server. The
[Reading OPC UA guide](/guides/opcua-client/) walks through this, with the
settings for an Ignition gateway and a CODESYS runtime, and lists the servers
it has been run against.

## Next

[Serve your tags over OPC UA](/learn/io-and-protocols/opcua-first-browse/) is the
other direction: your controller as the server.
