"""F-03: a Read of a node id in a namespace index the server does not have answers
Bad_NodeIdUnknown (OPC UA Part 4 Table 178), the same code the Write service gives and
the same code an unknown id in a known namespace gets. Project: evidence/typed.
Oracle: asyncua + Part 4. Reads; the writes target nodes that do not exist."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP) as c:
        for nid in ["ns=2;s=Level", "ns=3;s=Level", "ns=1;s=Nope", "ns=7;i=99"]:
            dv = await c.get_node(nid).read_data_value(raise_on_bad_status=False)
            ok(dv.StatusCode.name == "BadNodeIdUnknown", f"READ  {nid}: {dv.StatusCode.name} (0x{dv.StatusCode.value:08X})")
            p = ua.WriteParameters(); p.NodesToWrite = [ua.WriteValue(NodeId=ua.NodeId.from_string(nid), AttributeId=ua.AttributeIds.Value, Value=ua.DataValue(ua.Variant(1.0)))]
            r = (await c.uaclient.write(p))[0]
            ok(r.name == "BadNodeIdUnknown", f"WRITE {nid}: {r.name} (0x{r.value:08X})")
asyncio.run(main()); sys.exit(1 if F else 0)
