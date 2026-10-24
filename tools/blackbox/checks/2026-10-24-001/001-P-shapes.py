"""Pass: the node shapes the guide describes - scalar -> Variable; UDT -> Object with member
children, nested UDTs nest; array of scalars -> Variable with ValueRank 1 and
ArrayDimensions the declared length; array of UDTs -> Object with Pumps[1], Pumps[2] from
the declared lower bound; a UDT's array member -> Variable. Project: evidence/typed.
Oracle: asyncua."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
V, O = ua.NodeClass.Variable, ua.NodeClass.Object
async def kids(c, nid): return sorted([(await k.read_browse_name()).Name for k in await c.get_node(nid).get_children()])
async def main():
    async with Client(EP) as c:
        async def cls(nid): return await c.get_node(nid).read_node_class()
        ok(await cls("ns=1;s=Level") == V, "Level is a Variable")
        ok(await cls("ns=1;s=P101") == O, "P101 (UDT) is an Object")
        k = await kids(c, "ns=1;s=P101"); ok(k == ["Drive", "Fault", "Running", "Starts", "State"], f"P101 children {k}")
        ok(await cls("ns=1;s=P101.Drive") == O, "P101.Drive (nested UDT) is an Object")
        k = await kids(c, "ns=1;s=P101.Drive"); ok(k == ["Amps", "Speed"], f"P101.Drive children {k}")
        ok(await cls("ns=1;s=P101.Drive.Speed") == V, "P101.Drive.Speed is a Variable")
        for t, n in [("RealArr", 4), ("IntArr", 3), ("BoolArr", 3), ("StrArr", 2), ("TimeArr", 2)]:
            node = c.get_node(f"ns=1;s={t}"); vr = await node.read_value_rank(); ad = list(await node.read_array_dimensions() or []); v = await node.read_value()
            ok(await node.read_node_class() == V and vr == 1 and ad == [n] and len(v) == n, f"{t}: Variable vr={vr} ad={ad} len={len(v)}")
        ok(await cls("ns=1;s=Pumps") == O, "Pumps (array of UDT) is an Object")
        k = await kids(c, "ns=1;s=Pumps"); ok(k == ["Pumps[1]", "Pumps[2]"], f"Pumps children {k} (declared lower bound 1)")
        ok(await cls("ns=1;s=Pumps[2]") == O and await cls("ns=1;s=Pumps[2].Drive.Amps") == V, "Pumps[2] is an Object, Pumps[2].Drive.Amps a Variable")
        node = c.get_node("ns=1;s=Recipe.Steps"); vr = await node.read_value_rank(); ad = list(await node.read_array_dimensions() or [])
        ok(await node.read_node_class() == V and vr == 1 and ad == [3], f"Recipe.Steps: Variable vr={vr} ad={ad}")
        dv = await c.get_node("ns=1;s=Pumps[3]").read_data_value(raise_on_bad_status=False)
        ok(dv.StatusCode.name == "BadNodeIdUnknown", f"Pumps[3] (past the bound): {dv.StatusCode.name}")
asyncio.run(main()); sys.exit(1 if F else 0)
