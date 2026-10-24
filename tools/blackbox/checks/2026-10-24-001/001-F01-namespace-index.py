"""F-01: the project's tags live in namespace index 1, the entry after the standard
namespace in NamespaceArray, with URI urn:joyautomation:nautilus:<project>, and the
guide's example form ns=1;s=<tag> resolves. Project: evidence/typed. Oracle: asyncua.
Reads only."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP) as c:
        nsa = await c.get_namespace_array()
        print("NamespaceArray =", nsa)
        objs = c.get_objects_node()
        folders = [n for n in await objs.get_children() if n.nodeid.NamespaceIndex != 0]
        ok(len(folders) == 1, f"one project folder under Objects (got {[f.nodeid.to_string() for f in folders]})")
        name = (await folders[0].read_browse_name()).Name
        ok(len(nsa) >= 2 and nsa[1] == f"urn:joyautomation:nautilus:{name}", f"NamespaceArray[1] == urn:joyautomation:nautilus:{name}")
        ok(folders[0].nodeid.NamespaceIndex == 1, f"project folder is in ns=1 (got ns={folders[0].nodeid.NamespaceIndex})")
        dv = await c.get_node("ns=1;s=Level").read_data_value(raise_on_bad_status=False)
        ok(dv.StatusCode.is_good() and dv.Value.Value == 1.5, f"read ns=1;s=Level -> {dv.StatusCode.name} {dv.Value.Value if dv.Value else None}")
        dv = await c.get_node("ns=1;s=P101.Drive.Speed").read_data_value(raise_on_bad_status=False)
        ok(dv.StatusCode.is_good(), f"read ns=1;s=P101.Drive.Speed -> {dv.StatusCode.name}")
        dv = await c.get_node("ns=1;s=Pumps[2]").read_attribute(ua.AttributeIds.NodeClass, raise_on_bad_status=False)
        ok(dv.StatusCode.is_good(), f"ns=1;s=Pumps[2] is a node -> {dv.StatusCode.name}")
asyncio.run(main()); sys.exit(1 if F else 0)
