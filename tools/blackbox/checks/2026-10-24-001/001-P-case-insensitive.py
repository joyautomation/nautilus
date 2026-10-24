"""Pass: tag names are IEC identifiers, so a node id differing only in case resolves to
the tag (guide: ns=1;s=level resolves to Level). Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP) as c:
        for nid in ["ns=1;s=level", "ns=1;s=LEVEL", "ns=1;s=p101.drive.speed", "ns=1;s=pumps[2].drive.amps", "ns=1;s=str1"]:
            n = c.get_node(nid)
            dv = await n.read_data_value(raise_on_bad_status=False)
            bn = await n.read_attribute(ua.AttributeIds.BrowseName, raise_on_bad_status=False)
            ok(dv.StatusCode.is_good(), f"read {nid}: {dv.StatusCode.name} value={dv.Value.Value if dv.Value else None} browsename={bn.Value.Value.Name if bn.StatusCode.is_good() else '?'}")
        ref = await c.get_node("ns=1;s=Level").read_value(); low = await c.get_node("ns=1;s=level").read_value()
        ok(ref == low, f"Level ({ref}) == level ({low})")
asyncio.run(main()); sys.exit(1 if F else 0)
