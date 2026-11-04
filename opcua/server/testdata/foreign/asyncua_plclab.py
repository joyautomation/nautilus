"""A PLC-Lab-shaped client, from asyncua: drives examples/plclab.

Usage: asyncua_plclab.py <endpoint>

Does what MHJ PLC-Lab does with a CODESYS controller, as far as its manual
and a server PLC-Lab is known to accept show: connects on the None
endpoint, anonymously, with no certificate; finds the container by its
browse name, PlcLabConnector, wherever it sits under Objects; reads the
SIZE_ words; writes its area tables as UInt16; writes input bytes one at a
time as Byte, by string node ids built from the container's
(<container>.Inputs[0]); and polls the whole Outputs array. Each step
writes one input byte and polls Outputs until byte 0 is what the tank
logic should answer, or the step times out.

Prints one JSON object: what it found, what each step saw, and the access
levels of the nodes it writes.
"""

import asyncio
import json
import sys
import time

from asyncua import Client, ua

ENDPOINT = sys.argv[1]
POLL = 0.05  # PLC-Lab's default polling rate, 50 ms


def reason(exc):
    """The status code name an exception carries, else its text."""
    code = getattr(exc, "code", None)
    if isinstance(code, int):
        try:
            return ua.StatusCode(code).name
        except Exception:  # noqa: BLE001
            pass
    return f"{type(exc).__name__}: {exc}"


async def find(client, name):
    """The first node under Objects whose browse name is name, breadth first."""
    level = [client.nodes.objects]
    for _ in range(6):
        nxt = []
        for node in level:
            for child in await node.get_children():
                if child.nodeid == client.nodes.server.nodeid:
                    continue
                if (await child.read_browse_name()).Name == name:
                    return child
                nxt.append(child)
        level = nxt
    return None


async def main():
    client = Client(ENDPOINT, timeout=5)
    out = {"ok": False}
    try:
        async with client:
            out["namespaces"] = await client.get_namespace_array()
            conn = await find(client, "PlcLabConnector")
            if conn is None:
                out["error"] = "no PlcLabConnector under Objects"
                print(json.dumps(out), flush=True)
                return
            ns, base = conn.nodeid.NamespaceIndex, conn.nodeid.Identifier
            out["container"] = conn.nodeid.to_string()

            def node(path):
                return client.get_node(ua.NodeId(f"{base}.{path}", ns))

            out["sizes"] = {}
            for name in ("SIZE_AREA_I_ARRAY", "SIZE_AREA_Q_ARRAY", "SIZE_I_ARRAY", "SIZE_Q_ARRAY"):
                dv = await node(name).read_data_value()
                out["sizes"][name] = [dv.Value.Value, dv.Value.VariantType.name]

            # The area tables: one input byte and one output byte, connector
            # byte 0 to process image byte 0.
            for table in ("AreaInputs", "AreaOutputs"):
                for field, value in (("CntBytes", 1), ("Addr", 0), ("IoOfs", 0)):
                    await node(f"{table}[0].{field}").write_value(ua.Variant(value, ua.VariantType.UInt16))

            out["access"] = {}
            for path in ("Inputs[0]", "AreaInputs[0].CntBytes", "Outputs[0]"):
                n = node(path)
                out["access"][path] = [
                    (await n.read_attribute(ua.AttributeIds.AccessLevel)).Value.Value,
                    (await n.read_attribute(ua.AttributeIds.UserAccessLevel)).Value.Value,
                ]
            try:
                await node("Outputs[0]").write_value(ua.Variant(1, ua.VariantType.Byte))
                out["outputsWrite"] = "Good"
            except Exception as exc:  # noqa: BLE001
                out["outputsWrite"] = reason(exc)

            outputs = node("Outputs")
            # (input byte 0, the output byte 0 the tank logic answers with,
            # seconds to wait). Input bits: 1 start, 2 stop (normally closed),
            # 4 low switch, 8 high switch. Output bits: 1 fill, 2 drain,
            # 4 run lamp, 8 full lamp.
            steps = [
                (2, 0, 2),    # stop not pressed: idle
                (3, 5, 2),    # start: fill, run lamp
                (2, 5, 2),    # start released: sealed in
                (6, 5, 2),    # above the low switch: still filling
                (14, 12, 2),  # the high switch: full, fill closes
                (14, 14, 6),  # three seconds full: drain
                (2, 5, 2),    # below the low switch: fill again
                (0, 0, 2),    # stop pressed
            ]
            out["steps"] = []
            for value, want, wait in steps:
                await node("Inputs[0]").write_value(ua.Variant(value, ua.VariantType.Byte))
                start = time.monotonic()
                got = None
                while time.monotonic() - start < wait:
                    image = list(await outputs.read_value())
                    got = image[0]
                    if got == want:
                        break
                    await asyncio.sleep(POLL)
                back = await node("Inputs[0]").read_value()
                out["steps"].append({"input": value, "readBack": back, "want": want, "got": got,
                                     "ms": round((time.monotonic() - start) * 1000)})
            out["outputsLength"] = len(list(await outputs.read_value()))
            out["ok"] = True
    except Exception as exc:  # noqa: BLE001
        out["error"] = reason(exc)
    print(json.dumps(out), flush=True)


asyncio.run(main())
