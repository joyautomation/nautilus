"""Pass: the guide's type table, row by row, by each node's DataType attribute and the
Value's variant type, and the value equals the manifest's init. TIME is Duration (i=290)
carried as a Double of milliseconds; an enumeration is Int32 (the member's value).
Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
VT = ua.VariantType
# tag: (DataType node id, variant type, value)
T = {"Flag": ("i=1", VT.Boolean, True), "Small": ("i=2", VT.SByte, -5), "USmall": ("i=3", VT.Byte, 200), "Byte1": ("i=3", VT.Byte, 255),
     "Int1": ("i=4", VT.Int16, -1234), "UInt1": ("i=5", VT.UInt16, 60000), "Word1": ("i=5", VT.UInt16, 65535), "Dint1": ("i=6", VT.Int32, -100000),
     "UDint1": ("i=7", VT.UInt32, 4000000000), "DWord1": ("i=7", VT.UInt32, 4294967295), "Lint1": ("i=8", VT.Int64, -9000000000),
     "ULint1": ("i=9", VT.UInt64, 9000000000), "LWord1": ("i=9", VT.UInt64, 1), "Level": ("i=11", VT.Double, 1.5), "LReal1": ("i=11", VT.Double, 2.5),
     "Str1": ("i=12", VT.String, "hello"), "Time1": ("i=290", VT.Double, 2500.0), "Mode1": ("i=6", VT.Int32, 10),
     "P101.Running": ("i=1", VT.Boolean, True), "P101.Starts": ("i=6", VT.Int32, None), "P101.Drive.Speed": ("i=11", VT.Double, None), "P101.State": ("i=6", VT.Int32, 10),
     "RealArr": ("i=11", VT.Double, [10.0, 20.0, 0.0, 0.0]), "IntArr": ("i=4", VT.Int16, [1, 2, 3]), "BoolArr": ("i=1", VT.Boolean, [True, False, True]),
     "StrArr": ("i=12", VT.String, ["a", "b"]), "TimeArr": ("i=290", VT.Double, [1000.0, 2000.0]), "Recipe.TargetL": ("i=11", VT.Double, 10.0),
     "Recipe.SettleTime": ("i=290", VT.Double, 2000.0), "Recipe.Steps": ("i=290", VT.Double, [1000.0, 2000.0, 0.0]), "Pumps[2].Drive.Amps": ("i=11", VT.Double, None)}
async def main():
    async with Client(EP) as c:
        for t, (dt, vt, val) in T.items():
            n = c.get_node(f"ns=1;s={t}")
            got_dt = (await n.read_data_type()).to_string(); dv = await n.read_data_value()
            got_vt = dv.Value.VariantType; got = dv.Value.Value
            good = got_dt == dt and got_vt == vt and (val is None or got == val)
            ok(good, f"{t}: DataType={got_dt} variant={got_vt.name} value={got!r}  (want {dt} {vt.name} {val!r})")
asyncio.run(main()); sys.exit(1 if F else 0)
