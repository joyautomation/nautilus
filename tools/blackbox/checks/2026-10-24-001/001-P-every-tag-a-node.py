"""Pass: every tag in evidence/typed/nautilus.yaml is a child of the project folder, each
addressable as ns=1;s=<name>, and nothing else is. The expected list is the manifest's,
copied here so the check needs no YAML parser. Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
TAGS = ["Flag","Small","USmall","Byte1","Int1","UInt1","Word1","Dint1","UDint1","DWord1","Lint1","ULint1","LWord1","Level","LReal1","Str1","Time1","Mode1","P101","RealArr","IntArr","BoolArr","StrArr","TimeArr","Grid","Pumps","Recipe","UnitOnly","Counter","Ramp","Blink","Fixed","ScanDt"]
async def main():
    async with Client(EP) as c:
        folder = [n for n in await c.get_objects_node().get_children() if n.nodeid.NamespaceIndex != 0][0]
        kids = await folder.get_children()
        names = {(await k.read_browse_name()).Name: k.nodeid.to_string() for k in kids}
        ok(set(names) == set(TAGS), f"folder children == manifest tags ({len(names)}/{len(TAGS)}); extra={set(names)-set(TAGS)} missing={set(TAGS)-set(names)}")
        for t in TAGS:
            ok(names.get(t) == f"ns=1;s={t}", f"{t} -> {names.get(t)}")
            dv = await c.get_node(f"ns=1;s={t}").read_attribute(ua.AttributeIds.BrowseName, raise_on_bad_status=False)
            ok(dv.StatusCode.is_good() and dv.Value.Value.Name == t, f"ns=1;s={t} resolves: {dv.StatusCode.name}")
asyncio.run(main()); sys.exit(1 if F else 0)
