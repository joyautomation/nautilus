---
title: Coming from SCADA (Ignition, Kepware, Wonderware)
description: Where the tag providers, UDTs, alarm pipelines, views and OPC UA connections you know live in nautilus, and what is different when the controller is the platform.
sidebar:
  order: 0
---

If you build plant systems in Ignition, Kepware or Wonderware, you already
know most of what nautilus does: named tags, OPC UA, alarms, a historian and
operator screens. The difference is where they live. There is no gateway to
install and configure. A **controller** is one program that runs the logic,
holds the tags, serves the screens and speaks the protocols, and it is
configured by text files in a project folder that you keep in git.

## What maps to what

| You know | In nautilus | Read |
| --- | --- | --- |
| A tag provider, tags and folders | The controller's **tag store**; each tag declared in `nautilus.yaml` with a type and a **role** (`input`, `output`, `setpoint`, `state`) | [The tag model](/guides/tag-model/) |
| A UDT / data type | A `TYPE` with a `STRUCT` (or an enumeration); a tag takes it as its `type:`, and one member is written as `Tag.Member` | [The tag model](/guides/tag-model/) |
| An OPC UA connection to a PLC or gateway | An `opcua-client` driver; `naut opcua import` writes the bindings from the server's address space | [Reading OPC UA](/guides/opcua-client/) |
| Exposing tags to other systems over OPC UA | An `opcua:` section serves the controller's tags: browse, read, subscribe, and write only the tags you open with `writable:` | [Serving OPC UA](/guides/opcua-server/) |
| A device connection (Modbus, EtherNet/IP) | A driver in the manifest, with a committed device map | [Modbus](/guides/modbus/), [EtherNet/IP](/guides/ethernet-ip/) |
| An alarm pipeline | An `alarms:` section: conditions declared next to the tags, with acknowledge, shelve and a journal | [Alarms](/guides/alarms/) |
| A history provider and trends | `naut historian` archives tags into Postgres; the controller serves the history back for trends | [Historian](/guides/historian/) |
| A Perspective view or Vision window | A `*.mimic.json` operator screen: equipment, pipes and tag bindings in a file, edited graphically in VS Code and served by `naut run` | [Operator screens](/guides/operator-screens/) |
| A script that reads and writes tags | An HTTP API (and an event stream) any program can call; the control logic itself is IEC 61131-3 | [The HTTP API](/guides/http-api/) |
| Gateway security and roles | Roles, users and sign-in are being added | [Users, roles and sign-in](/guides/auth/) |

## What is different

- **The controller is the platform.** There is no separate gateway, designer
  or project database. `naut run` starts the controller on your laptop;
  `naut build` makes one program; `naut deploy` puts it on a Linux box.
- **Configuration is files.** A change is a commit you can diff and review,
  and the whole project builds in continuous integration.
- **Control logic is part of the same project.** If you have only read
  ladder, [Studio 5000 to nautilus](/coming-from/studio-5000/) shows the
  instruction map, and [your first program](/guides/first-program/) writes a
  small one end to end. You do not need to write logic to put a screen on a
  process: `naut new` makes a project with a simulated plant that moves.
- **Quality is explicit.** A tag whose source is down keeps its last value
  and carries a quality (`stale`, `notConnected`) that you can read from the
  API. See the Modbus and OPC UA pages for what each driver reports.
- **Screens are files too.** The `Diagnostics` and `Alarms` pages come with
  every served screen.

## Where to start

1. [Install the CLI and the editor](/learn/getting-started/install/).
2. [Your first controller, running](/learn/getting-started/first-controller/):
   a project with a moving process in one command.
3. [Operator screens](/guides/operator-screens/): put a screen on it.
4. [Serving OPC UA](/guides/opcua-server/): browse its tags from your own
   OPC UA client.
