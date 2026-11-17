#!/usr/bin/env python3
"""The foreign-implementation client for a mapped address space.

A virtual device's server lays the device's own node ids and tree over the
tag store (opcua/server/mapped.go). This is asyncua doing what a SCADA
gateway does against one: read the namespace array, browse Objects to the
device's tree, translate a browse path to the device's node id, read a
numeric node in another namespace, subscribe to it and see a change, and
write a writable node while a read-only one refuses.

Driven by opcua/server/foreign_test.go (TestForeignAsyncuaMapped), with the
same stdio handshake as asyncua_client.py: {"ready": true} once subscribed,
then the Go side changes the tag and the script prints its report.

    asyncua_mapped.py <endpoint-url>
"""

import asyncio
import json
import sys

from asyncua import Client, ua


class Changes:
    def __init__(self):
        self.queue = asyncio.Queue()

    def datachange_notification(self, node, val, data):
        self.queue.put_nowait(val)


async def main(url):
    report = {}
    async with Client(url=url, timeout=10) as client:
        report["namespaces"] = await client.get_namespace_array()

        objects = client.nodes.objects
        names = {}
        for child in await objects.get_children():
            bn = await child.read_browse_name()
            names[bn.Name] = child.nodeid.to_string()
        report["objects"] = names

        target = await objects.get_child(["1:IOLM", "1:Port 1", "1:SIO Input"])
        report["translated"] = target.nodeid.to_string()

        pin4 = client.get_node("ns=3;i=8012")
        dv = await pin4.read_data_value()
        report["pin4"] = {"value": dv.Value.Value, "variant": dv.Value.VariantType.name,
                          "status": dv.StatusCode.name}
        report["pin4_name"] = (await pin4.read_browse_name()).Name

        changes = Changes()
        sub = await client.create_subscription(20, changes)
        await sub.subscribe_data_change(pin4)
        await changes.queue.get()  # the initial value
        print(json.dumps({"ready": True}), flush=True)
        report["changed_to"] = await asyncio.wait_for(changes.queue.get(), 10)

        pdo = client.get_node("ns=1;s=IOLM/Port 1/PDO")
        await pdo.write_value(ua.DataValue(ua.Variant(7, ua.VariantType.Int32)))
        report["pdo"] = await pdo.read_value()

        status = client.get_node("ns=1;s=IOLM/Port 1/Status")
        try:
            await status.write_value(ua.DataValue(ua.Variant("X", ua.VariantType.String)))
            report["status_write"] = "Good"
        except ua.UaStatusCodeError as e:
            report["status_write"] = type(e).__name__

    print(json.dumps(report), flush=True)


if __name__ == "__main__":
    asyncio.run(main(sys.argv[1]))
