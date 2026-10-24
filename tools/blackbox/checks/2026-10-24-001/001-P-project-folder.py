"""Pass: under Objects there is exactly one project folder, in the project's namespace,
BrowseName and DisplayName the project name, Description naming the controller; the other
children of Objects are standard (ns=0). Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP) as c:
        kids = await c.get_objects_node().get_children()
        std = [k.nodeid.to_string() for k in kids if k.nodeid.NamespaceIndex == 0]
        own = [k for k in kids if k.nodeid.NamespaceIndex != 0]
        print("Objects children (ns=0):", std)
        ok("i=2253" in std, "the standard Server object is a child of Objects")
        ok(len(own) == 1, f"exactly one non-standard child (got {len(own)})")
        f = own[0]
        bn = (await f.read_browse_name()).Name; dn = (await f.read_display_name()).Text; desc = (await f.read_description()).Text; nc = await f.read_node_class()
        print(f"folder {f.nodeid.to_string()} class={nc.name} bn={bn!r} dn={dn!r} desc={desc!r}")
        ok(nc == ua.NodeClass.Object, "folder is an Object")
        ok(bn == dn == "typed", "BrowseName == DisplayName == project name")
        ok(desc and "typed" in desc and "Nautilus" in desc, "Description names the controller")
asyncio.run(main()); sys.exit(1 if F else 0)
