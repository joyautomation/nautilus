"""Pass: the guide's type table, row by row, by each node's DataType attribute and the
Value's variant type, and the value equals the manifest's init. TIME is Duration (i=290)
carried as a Double of milliseconds. An enumeration tag's DataType is any DataType that is a
subtype of Enumeration (i=29), found by browsing HasSubtype down from i=29 on the server; its
Value is an Int32 (the member's value). Every tag of one enumeration TYPE has the same DataType.
Rewritten 2026-11-03 per runs/RECONCILIATION-2026-11-03.md ruling 1 (was: enumeration = i=6).
Project: evidence/typed. Oracle: asyncua + Part 3 (DataType hierarchy, Enumeration i=29)."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
VT = ua.VariantType
ENUM = "subtype of i=29"   # the expected DataType of an enumeration tag
# tag: (DataType node id, variant type, value)
T = {"Flag": ("i=1", VT.Boolean, True), "Small": ("i=2", VT.SByte, -5), "USmall": ("i=3", VT.Byte, 200), "Byte1": ("i=3", VT.Byte, 255),
     "Int1": ("i=4", VT.Int16, -1234), "UInt1": ("i=5", VT.UInt16, 60000), "Word1": ("i=5", VT.UInt16, 65535), "Dint1": ("i=6", VT.Int32, -100000),
     "UDint1": ("i=7", VT.UInt32, 4000000000), "DWord1": ("i=7", VT.UInt32, 4294967295), "Lint1": ("i=8", VT.Int64, -9000000000),
     "ULint1": ("i=9", VT.UInt64, 9000000000), "LWord1": ("i=9", VT.UInt64, 1), "Level": ("i=11", VT.Double, 1.5), "LReal1": ("i=11", VT.Double, 2.5),
     "Str1": ("i=12", VT.String, "hello"), "Time1": ("i=290", VT.Double, 2500.0), "Mode1": (ENUM, VT.Int32, 10),
     "P101.Running": ("i=1", VT.Boolean, True), "P101.Starts": ("i=6", VT.Int32, None), "P101.Drive.Speed": ("i=11", VT.Double, None), "P101.State": (ENUM, VT.Int32, 10),
     "RealArr": ("i=11", VT.Double, [10.0, 20.0, 0.0, 0.0]), "IntArr": ("i=4", VT.Int16, [1, 2, 3]), "BoolArr": ("i=1", VT.Boolean, [True, False, True]),
     "StrArr": ("i=12", VT.String, ["a", "b"]), "TimeArr": ("i=290", VT.Double, [1000.0, 2000.0]), "Recipe.TargetL": ("i=11", VT.Double, 10.0),
     "Recipe.SettleTime": ("i=290", VT.Double, 2000.0), "Recipe.Steps": ("i=290", VT.Double, [1000.0, 2000.0, 0.0]), "Pumps[2].Drive.Amps": ("i=11", VT.Double, None)}
async def subtypes(c, nid):
    """Every DataType below nid, by browsing HasSubtype (forward, i=45) recursively."""
    out, todo = set(), [nid]
    while todo:
        refs = await c.get_node(todo.pop()).get_references(refs=ua.ObjectIds.HasSubtype, direction=ua.BrowseDirection.Forward, includesubtypes=False)
        for r in refs:
            k = r.NodeId.to_string()
            if k not in out: out.add(k); todo.append(k)
    return out
async def main():
    async with Client(EP) as c:
        enums = await subtypes(c, "i=29"); enum_dts = {}
        print(f"HasSubtype below Enumeration (i=29): {len(enums)} DataTypes; ns!=0: {sorted(e for e in enums if not e.startswith('i='))}")
        for t, (dt, vt, val) in T.items():
            n = c.get_node(f"ns=1;s={t}")
            got_dt = (await n.read_data_type()).to_string(); dv = await n.read_data_value()
            got_vt = dv.Value.VariantType; got = dv.Value.Value
            dt_ok = (got_dt in enums) if dt == ENUM else got_dt == dt
            if dt == ENUM: enum_dts[t] = got_dt
            good = dt_ok and got_vt == vt and (val is None or got == val)
            ok(good, f"{t}: DataType={got_dt} variant={got_vt.name} value={got!r}  (want {dt} {vt.name} {val!r})")
        ok(len(set(enum_dts.values())) == 1, f"both tags of TYPE Mode share one DataType: {enum_dts}")
asyncio.run(main()); sys.exit(1 if F else 0)
