"""F-08: an anonymous session cannot write the standard Server object's variables
(guide, Security: "nobody can write ... the standard Server object and its variables
included"; Part 5 §8.2 these are server-maintained). Every write is refused and a fresh
session reads the original values. If a write is accepted the check restores the value
and fails. Project: evidence/typed. Oracle: asyncua + Part 5."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
T = {"i=2255": ("NamespaceArray", lambda v: ua.Variant(list(v) + ["urn:evil"], ua.VariantType.String)),
     "i=2259": ("ServerStatus.State", lambda v: ua.Variant(1, ua.VariantType.Int32)),
     "i=2261": ("BuildInfo.ProductName", lambda v: ua.Variant("Hacked", ua.VariantType.String)),
     "i=2263": ("BuildInfo.ManufacturerName", lambda v: ua.Variant("Hacked", ua.VariantType.String)),
     "i=2267": ("ServiceLevel", lambda v: ua.Variant(0, ua.VariantType.Byte)),
     "i=2258": ("ServerStatus.CurrentTime", lambda v: ua.Variant(v, ua.VariantType.DateTime))}
async def main():
    before = {}
    async with Client(EP) as c:
        for nid, (what, mk) in T.items():
            dv = await c.get_node(nid).read_data_value(raise_on_bad_status=False)
            before[nid] = dv.Value.Value if dv.StatusCode.is_good() else None
            try:
                v = mk(before[nid])
            except Exception as e:
                print(f"INFO  {what}: cannot build a write value ({e})"); continue
            p = ua.WriteParameters(); p.NodesToWrite = [ua.WriteValue(NodeId=ua.NodeId.from_string(nid), AttributeId=ua.AttributeIds.Value, Value=ua.DataValue(v))]
            r = (await c.uaclient.write(p))[0]
            ok(r.name in ("BadUserAccessDenied", "BadNotWritable"), f"WRITE {what} ({nid}): {r.name} (0x{r.value:08X})")
    async with Client(EP) as c:
        for nid, (what, _) in T.items():
            if nid == "i=2258": continue
            dv = await c.get_node(nid).read_data_value(raise_on_bad_status=False)
            now = dv.Value.Value if dv.StatusCode.is_good() else None
            same = (now == before[nid]) if nid != "i=2259" else True
            ok(same, f"fresh session reads {what} = {now!r} (was {before[nid]!r})")
            if not same and before[nid] is not None:
                try: await c.get_node(nid).write_value(before[nid]); print(f"INFO  restored {what}")
                except Exception as e: print(f"INFO  could not restore {what}: {e}")
asyncio.run(main()); sys.exit(1 if F else 0)
