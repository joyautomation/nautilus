"""Pass: every tag Variable has AccessLevel == UserAccessLevel == CurrentRead (1); a Write to
a scalar, a BOOL, a UDT member and an array is answered Bad_UserAccessDenied and the value
is unchanged; a Write to the folder or a UDT Object, or to a Description attribute, is
Bad_NotWritable. Project: evidence/typed. Oracle: asyncua + guide (Security)."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
A = ua.AttributeIds
async def walk(n, out):
    for k in await n.get_children():
        if await k.read_node_class() == ua.NodeClass.Variable: out.append(k)
        else: await walk(k, out)
async def write(c, nid, attr, variant):
    p = ua.WriteParameters(); p.NodesToWrite = [ua.WriteValue(NodeId=ua.NodeId.from_string(nid), AttributeId=attr, Value=ua.DataValue(variant))]
    return (await c.uaclient.write(p))[0]
async def main():
    async with Client(EP) as c:
        folder = [n for n in await c.get_objects_node().get_children() if n.nodeid.NamespaceIndex != 0][0]
        vs = []; await walk(folder, vs)
        bad = []
        for v in vs:
            al = (await v.read_attribute(A.AccessLevel)).Value.Value; ual = (await v.read_attribute(A.UserAccessLevel)).Value.Value
            if not (al == 1 and ual == 1): bad.append((v.nodeid.to_string(), al, ual))
        ok(len(vs) > 30 and not bad, f"{len(vs)} Variables all AccessLevel=UserAccessLevel=1; exceptions {bad}")
        for nid, var in [("ns=1;s=Level", ua.Variant(9.9)), ("ns=1;s=Flag", ua.Variant(False)), ("ns=1;s=P101.Drive.Speed", ua.Variant(9.9)), ("ns=1;s=IntArr", ua.Variant([9, 9, 9], ua.VariantType.Int16)), ("ns=1;s=level", ua.Variant(9.9))]:
            before = await c.get_node(nid).read_value()
            r = await write(c, nid, A.Value, var)
            after = await c.get_node(nid).read_value()
            ok(r.name == "BadUserAccessDenied" and after == before, f"WRITE {nid}: {r.name} (0x{r.value:08X}); value {before!r} -> {after!r}")
        for nid in [folder.nodeid.to_string(), "ns=1;s=P101", "ns=1;s=Pumps"]:
            r = await write(c, nid, A.Value, ua.Variant(1.0))
            ok(r.name == "BadNotWritable", f"WRITE Value of Object {nid}: {r.name}")
        r = await write(c, "ns=1;s=Level", A.Description, ua.Variant(ua.LocalizedText("x")))
        ok(r.name == "BadNotWritable", f"WRITE Description of Level: {r.name}")
asyncio.run(main()); sys.exit(1 if F else 0)
