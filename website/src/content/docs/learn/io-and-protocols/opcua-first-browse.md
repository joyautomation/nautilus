---
title: Serve your tags over OPC UA
description: "Add one section to the project and browse, read and watch the controller's tags from a standard OPC UA client."
sidebar:
  order: 3
---

OPC UA is how a plant's SCADA, historian and other systems read a controller.
In this lesson your controller becomes an OPC UA **server**: one section in
`nautilus.yaml`, and any OPC UA client can browse its tags. About ten
minutes. You need the `hello` project from [your first
controller](/learn/getting-started/first-controller/).

## 1. Turn the server on

In `nautilus.yaml`, add an `opcua:` section, and give `Setpoint` a unit and a
description (they travel to the client):

```yaml
tags:
  - { name: Sensor,   role: input,    type: REAL, init: 0.0 }
  - { name: Setpoint, role: setpoint, type: REAL, init: 50.0, unit: "%", desc: Target level }
  - { name: Actuator, role: output,   type: REAL }

opcua:
  endpoint: opc.tcp://127.0.0.1:4840
```

`127.0.0.1` keeps the server on your own computer, which is right for a
lesson; `opc.tcp://0.0.0.0:4840` would answer every machine on the network.

```sh
naut run
```

The first line now also says `opc ua on opc.tcp://127.0.0.1:4840`, and the
log says it generated a **server certificate** into `pki/`. OPC UA uses
certificates to identify servers and clients; the folder is kept out of git by
the scaffold's `.gitignore`.

## 2. Browse it from a foreign client

Any OPC UA client works the same way. This lesson uses **asyncua**, a Python
library that has nothing to do with nautilus, so you see what a plant system
would see:

```sh
pip install asyncua
```

Save as `browse.py`:

```python
import asyncio
from asyncua import Client

async def main():
    async with Client("opc.tcp://127.0.0.1:4840") as client:
        for folder in await client.nodes.objects.get_children():
            if (await folder.read_browse_name()).Name != "hello":  # the project's folder
                continue
            for tag in await folder.get_children():
                print((await tag.read_browse_name()).Name, "=", await tag.read_value(),
                      "|", (await tag.read_description()).Text)

asyncio.run(main())
```

```sh
python browse.py
```

```text
Actuator = 50.0 | None
Sensor = 0.0 | None
Setpoint = 50.0 | Target level [%]
```

Under `Objects` there is one folder named for the project, and in it one
variable per tag, with its type, its current value and its description. The
unit is added to the description in brackets. A tag with no `desc:` has no
description, as `Actuator` shows.

## 3. See a live change

Change the setpoint from the controller's HTTP API, and read it again:

```sh
curl -s -X POST localhost:8080/api/tags -d '{"name":"Setpoint","value":70}'
python browse.py
```

```text
Actuator = 70.0 | None
Sensor = 0.0 | None
Setpoint = 70.0 | Target level [%]
```

The client sees the controller's values as they are now. A client can also
**subscribe**, so the server tells it when a value changes instead of the
client asking again and again.

## Read-only by default

The client can read everything, and write nothing: a write comes back
`BadUserAccessDenied`. You open a tag for writing by name, in a `writable:`
list, and a client must be **trusted** (its certificate approved) before it
can use a secure connection to write. That is the right default for a
controller; the [OPC UA server guide](/guides/opcua-server/) shows both.

## Next

The [OPC UA server guide](/guides/opcua-server/) covers types, quality,
security and writes. To read another OPC UA server instead, see [Reading
OPC UA](/guides/opcua-client/).
