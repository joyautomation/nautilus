"""Anonymous writes on the None endpoint, from asyncua: the lab setup.

Usage: asyncua_anonymous.py <endpoint>

Connects the way a lab simulator does (PLC-Lab, Factory I/O): no security,
no certificate, an anonymous identity. Writes with asyncua's plain
write_value (a Good StatusCode, a SourceTimestamp, an Int64 for a Python
int), and reads UserAccessLevel. Prints one JSON object: what each write
did, and the access levels.
"""

import asyncio
import json
import sys

from asyncua import Client, ua

ENDPOINT = sys.argv[1]


def reason(exc):
    """The status code name an exception carries, else its text."""
    code = getattr(exc, "code", None)
    if isinstance(code, int):
        try:
            return ua.StatusCode(code).name
        except Exception:  # noqa: BLE001
            pass
    return f"{type(exc).__name__}: {exc}"


async def outcome(node, value):
    """write_value, then the value read back, or the status it was refused."""
    try:
        await node.write_value(value)
    except Exception as exc:  # noqa: BLE001
        return {"error": reason(exc)}
    got = await node.read_value()
    if not (isinstance(got, (bool, int, float, str)) and got == got):
        got = repr(got)
    return {"ok": True, "value": got}


WRITES = [
    ("Level", 4.25),
    ("Count", 7),  # a Python int: an Int64 Variant to an Int32 node
    ("Running", False),
    ("P101.Drive.Speed", 1450.0),
    # refused
    ("Unmatched", ("P101.Running", True)),
    ("CountTooBig", ("Count", 2**40)),
    ("Array", ("Levels", ua.Variant([9.0, 9.0, 9.0], ua.VariantType.Double))),
]


async def main():
    client = Client(ENDPOINT, timeout=5)
    try:
        async with client:
            out = {}
            for name, spec in WRITES:
                addr, value = spec if isinstance(spec, tuple) else (name, spec)
                out[name] = await outcome(client.get_node(f"ns=1;s={addr}"), value)
            access = {}
            for addr in ("Level", "P101.Running"):
                node = client.get_node(f"ns=1;s={addr}")
                access[addr] = {
                    "accessLevel": (await node.read_attribute(ua.AttributeIds.AccessLevel)).Value.Value,
                    "userAccessLevel": (await node.read_attribute(ua.AttributeIds.UserAccessLevel)).Value.Value,
                }
            print(json.dumps({"ok": True, "writes": out, "access": access}), flush=True)
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"ok": False, "error": reason(exc)}), flush=True)


asyncio.run(main())
