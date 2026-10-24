#!/usr/bin/env python3
"""The foreign-implementation client for nautilus's OPC UA server.

Everything in the Go tests talks gopcua to gopcua: the same codec on both
ends, so an encoding both halves get wrong the same way would pass. This
script is somebody else's stack — asyncua, the Python OPC UA
implementation — doing what a SCADA gateway or UAExpert does against the
server: discover endpoints, open a session, browse Objects → project →
tags → UDT members, read values with their data types, status and
timestamps, subscribe to a tag and see a change arrive, and have a write
refused.

Driven by opcua/server/foreign_test.go. The handshake is on stdio: the
script prints one JSON line {"ready": true} once its subscription is in
place, the Go side then changes the subscribed tag, and the script prints
its whole report as one JSON line and exits. Anything on stderr is a
failure the Go test shows.

    asyncua_client.py <endpoint-url> <project-folder-name>
"""

import asyncio
import json
import sys

from asyncua import Client, ua


class Changes:
    """Collects data-change notifications in the order they arrive."""

    def __init__(self):
        self.queue = asyncio.Queue()

    def datachange_notification(self, node, val, data):
        item = data.monitored_item
        self.queue.put_nowait({
            "value": val,
            "status": item.Value.StatusCode.name,
            "sourceTimestamp": item.Value.SourceTimestamp is not None,
        })


def describe(dv):
    return {
        "value": dv.Value.Value,
        "variant": dv.Value.VariantType.name,
        "isArray": dv.Value.is_array,
        "status": dv.StatusCode.name,
        "sourceTimestamp": dv.SourceTimestamp is not None,
        "serverTimestamp": dv.ServerTimestamp is not None,
    }


async def children_by_name(node):
    out = {}
    for child in await node.get_children():
        bn = await child.read_browse_name()
        out[bn.Name] = child
    return out


async def main(url, folder_name):
    report = {}
    async with Client(url=url) as client:
        ns = await client.get_namespace_array()
        report["namespaces"] = ns
        objects = await children_by_name(client.nodes.objects)
        report["objects"] = sorted(objects)
        folder = objects[folder_name]
        report["folderClass"] = (await folder.read_node_class()).name
        tags = await children_by_name(folder)
        report["tags"] = sorted(tags)
        report["classes"] = {name: (await node.read_node_class()).name for name, node in tags.items()}

        level = tags["Level"]
        report["Level"] = describe(await level.read_data_value())
        report["Level"]["dataType"] = (await level.read_data_type()).Identifier
        report["Level"]["description"] = (await level.read_description()).Text
        report["Level"]["accessLevel"] = sorted(a.name for a in await level.get_access_level())

        count = tags["Count"]
        report["Count"] = describe(await count.read_data_value())
        report["Count"]["dataType"] = (await count.read_data_type()).Identifier

        elapsed = tags["Elapsed"]
        report["Elapsed"] = describe(await elapsed.read_data_value())
        report["Elapsed"]["dataType"] = (await elapsed.read_data_type()).Identifier

        p101 = tags["P101"]
        members = await children_by_name(p101)
        report["P101"] = sorted(members)
        drive = await children_by_name(members["Drive"])
        report["P101.Drive"] = sorted(drive)
        # Address a member directly by its string node id, as a client that
        # knows the nautilus address would.
        idx = report["namespaces"].index(next(n for n in ns if n.startswith("urn:joyautomation:nautilus:")))
        speed = client.get_node(ua.NodeId("P101.Drive.Speed", idx))
        report["Speed"] = describe(await speed.read_data_value())
        report["Speed"]["browseName"] = (await speed.read_browse_name()).Name
        parent = await speed.get_parent()
        report["Speed"]["parent"] = parent.nodeid.Identifier if parent is not None else None

        levels = tags["Levels"]
        report["Levels"] = describe(await levels.read_data_value())
        report["Levels"]["valueRank"] = await levels.read_value_rank()
        report["Levels"]["arrayDimensions"] = await levels.read_array_dimensions()

        pumps = await children_by_name(tags["Pumps"])
        report["Pumps"] = sorted(pumps)

        # A node that does not exist.
        try:
            await client.get_node(ua.NodeId("Nope", idx)).read_value()
            report["unknown"] = "readable"
        except ua.UaStatusCodeError as e:
            report["unknown"] = ua.StatusCode(e.code).name

        # Subscribe, hand over to the Go side, wait for the change.
        changes = Changes()
        sub = await client.create_subscription(50, changes)
        await sub.subscribe_data_change(level)
        initial = await asyncio.wait_for(changes.queue.get(), 10)
        print(json.dumps({"ready": True}), flush=True)
        changed = await asyncio.wait_for(changes.queue.get(), 15)
        report["subscription"] = {"initial": initial, "changed": changed}
        await sub.delete()

        # A write is refused, not swallowed.
        try:
            await level.write_value(ua.DataValue(ua.Variant(99.0, ua.VariantType.Double)))
            report["write"] = "accepted"
        except ua.UaStatusCodeError as e:
            report["write"] = ua.StatusCode(e.code).name
        report["LevelAfterWrite"] = await level.read_value()

    print(json.dumps(report, default=str), flush=True)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    asyncio.run(main(sys.argv[1], sys.argv[2]))
