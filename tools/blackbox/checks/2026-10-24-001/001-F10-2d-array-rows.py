"""F-10: a two-dimensional array (Grid : ARRAY[0..1, 0..2] OF INT) is served as the guide
now describes - an Object of one-dimensional rows Grid[0], Grid[1], each a Variable of
Int16 with ValueRank 1 and ArrayDimensions [3]. Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP) as c:
        g = c.get_node("ns=1;s=Grid")
        ok((await g.read_node_class()) == ua.NodeClass.Object, "Grid is an Object")
        kids = await g.get_children()
        names = sorted([(await k.read_browse_name()).Name for k in kids])
        ok(names == ["Grid[0]", "Grid[1]"], f"Grid children {names}")
        for k in kids:
            nc = await k.read_node_class(); dt = await k.read_data_type(); vr = await k.read_value_rank(); ad = await k.read_array_dimensions(); v = await k.read_value()
            ok(nc == ua.NodeClass.Variable and dt.to_string() == "i=4" and vr == 1 and list(ad or []) == [3] and len(v) == 3,
               f"{k.nodeid.to_string()} {nc.name} dt={dt.to_string()} vr={vr} ad={ad} value={v}")
asyncio.run(main()); sys.exit(1 if F else 0)
