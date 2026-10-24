"""Pass: a tag's desc: and unit: become the node's Description in the form the guide gives,
`Wet well level [ft]`; desc alone is the desc; unit alone is `[unit]`; a member of a UDT
without desc has none. Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
WANT = {"Level": "Wet well level [ft]", "Flag": "A boolean", "Int1": "An INT [counts]", "UnitOnly": "[psi]"}
async def main():
    async with Client(EP) as c:
        for t, w in WANT.items():
            d = await c.get_node(f"ns=1;s={t}").read_description()
            ok((d.Text or "") == w, f"{t}: Description={d.Text!r} want {w!r}")
        d = await c.get_node("ns=1;s=Small").read_description()
        print(f"INFO  Small (no desc, no unit): Description={d.Text!r}")
        d = await c.get_node("ns=1;s=P101").read_description()
        print(f"INFO  P101 (UDT Object, no desc): Description={d.Text!r}")
asyncio.run(main()); sys.exit(1 if F else 0)
