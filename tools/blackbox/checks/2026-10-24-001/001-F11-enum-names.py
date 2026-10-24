"""F-11 (filed nautilus#425, gap): an enumeration tag keeps its member names on the wire -
a DataType that is an Enumeration subtype with EnumStrings/EnumValues, or an EnumStrings
property on the node (Part 3 §8.40, Part 8 §5.3.3). The guide promises only Int32, so
this check states the stronger expectation and fails until the gap is closed.
Project: evidence/typed. Oracle: asyncua + Part 3/8."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP) as c:
        n = c.get_node("ns=1;s=Mode1"); dt = await n.read_data_type(); v = await n.read_value()
        props = [(await p.read_browse_name()).Name for p in await n.get_properties()]
        print(f"Mode1: DataType={dt.to_string()} value={v!r} properties={props}")
        named = False
        if dt.NamespaceIndex != 0:
            dtn = c.get_node(dt); sup = [(await p.read_browse_name()).Name for p in await dtn.get_properties()]
            print(f"DataType node {dt.to_string()} properties={sup}"); named = bool({"EnumStrings", "EnumValues"} & set(sup))
        named = named or bool({"EnumStrings", "EnumValues"} & set(props))
        ok(named, "member names are available (Enumeration DataType with EnumStrings/EnumValues, or an EnumStrings property)")
asyncio.run(main()); sys.exit(1 if F else 0)
