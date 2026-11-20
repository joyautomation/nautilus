---
title: Publish your tags over MQTT (Sparkplug B)
description: "Turn a controller into a Sparkplug B edge node that tells an MQTT broker what tags it has and sends only what changes, and look at what a host would see."
sidebar:
  order: 4
---

Sparkplug B is the convention for sending a plant's data over MQTT: a
controller (an **edge node**) announces every tag it has, with types and
units, then sends only what changes. Any Sparkplug-aware host, Ignition for
one, discovers the tags from that announcement instead of having them
entered by hand. About fifteen minutes. You need the `hello` project from
[your first controller](/learn/getting-started/first-controller/) and an MQTT
broker.

## A broker for the lab

A standard MQTT broker is all it needs. For a lab, install Mosquitto and allow
anonymous connections, which a real plant never should:

```text
# /etc/mosquitto/conf.d/lab.conf
listener 1883 0.0.0.0
allow_anonymous true
```

(Restart it after the change. Real brokers use a user name, and a
`NAUTILUS_MQTT_PASSWORD` for the password; the Sparkplug guide covers TLS.)

## Turn the node on

Add a `sparkplug:` section to `nautilus.yaml`, with your broker's address:

```yaml
sparkplug:
  broker: tcp://10.0.0.5:1883
  group-id: Plant
  edge-node: hello
  writable: ["Setpoint"]
```

- `group-id` and `edge-node` name this controller in the MQTT topics: a plant
  is a *group*, and each controller in it a *node*. Only `broker` and
  `group-id` are required; the node name defaults to the project name.
- `writable` lists the tags a host may write back. Leave it out and the
  node is read-only.

```sh
naut run
```

The log says `sparkplug: born group=Plant node=hello` and the start-up line
ends `sparkplug up`. The controller announced itself with a **birth**.

## What the broker carries

The node publishes to topics named `spBv1.0/<group>/<message>/<node>`. With
`mosquitto_sub -t 'spBv1.0/#' -v` running before the node starts you see:

```text
spBv1.0/Plant/NBIRTH/hello
spBv1.0/Plant/NDATA/hello
spBv1.0/Plant/NDATA/hello
```

`NBIRTH` is the announcement, sent once when the node connects: every tag,
its type and its current value. `NDATA` follows each time a value changes,
carrying only what changed (**report by exception**), so a quiet plant sends
almost nothing. The payloads are binary (Protocol Buffers), which is why you
use a tool that decodes them.

## Look at what a host sees

`naut` can listen as a host and print what it discovers. Start it **first**
(Sparkplug forbids retained births, so a late joiner hears nothing, and the
tool asks only nodes it has already heard from to birth again), then start the controller:

```sh
naut sparkplug browse --broker tcp://10.0.0.5:1883 --group Plant --listen 20s
```

```text
NBIRTH Plant/hello (5 metrics)
  Actuator                                   Double
  Node Control/Rebirth                       Boolean
  Sensor                                     Double
  Setpoint                                   Double
  bdSeq                                      Int64
```

Your three tags are there with their types, and two metrics Sparkplug itself
needs: `bdSeq`, a counter that pairs each birth with its death certificate,
and `Node Control/Rebirth`, which a host sets to ask the node to announce
itself again. The `unit:` and `desc:` you declare travel with each metric, so
the SCADA side does not re-enter them.

## Next

The [Sparkplug guide](/guides/sparkplug/) covers how types and UDTs map, publish
classes (a deadband and a maximum interval per tag), devices, store-and-forward
for a broker that goes away, and writes from a host. To read another node's
tags into a controller, see the [Sparkplug host guide](/guides/sparkplug-host/).
