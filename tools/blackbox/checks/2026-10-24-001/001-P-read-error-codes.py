"""Pass: Read answers the Part 4 codes - Bad_NodeIdUnknown for an id that is not a node,
Bad_AttributeIdInvalid for Value of an Object (UDT, array-of-UDT, folder).
Project: evidence/typed. Oracle: asyncua + Part 4."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP) as c:
        folder = [n for n in await c.get_objects_node().get_children() if n.nodeid.NamespaceIndex != 0][0]
        for nid, want in [("ns=1;s=Nope", "BadNodeIdUnknown"), ("ns=1;s=Pumps[3]", "BadNodeIdUnknown"), ("ns=1;s=P101.Nope", "BadNodeIdUnknown"), ("ns=1;i=424242", "BadNodeIdUnknown"),
                          ("ns=1;s=P101", "BadAttributeIdInvalid"), ("ns=1;s=Pumps", "BadAttributeIdInvalid"), (folder.nodeid.to_string(), "BadAttributeIdInvalid")]:
            dv = await c.get_node(nid).read_data_value(raise_on_bad_status=False)
            ok(dv.StatusCode.name == want, f"READ {nid}: {dv.StatusCode.name} (want {want})")
        dv = await c.get_node("ns=1;s=RealArr[1]").read_data_value(raise_on_bad_status=False)
        print(f"INFO  READ ns=1;s=RealArr[1] (an element id the guide does not promise): {dv.StatusCode.name} {dv.Value.Value if dv.Value else None}")
asyncio.run(main()); sys.exit(1 if F else 0)
