"""F-22: SourceTimestamp of a UDT member moves only when that member's value does (guide,
Quality and timestamps), not when a sibling member is written. P101.Drive.Amps and
P101.Fault are never written; P101.Drive.Speed is written every scan. Two reads 1 s apart.
Project: evidence/typed. Oracle: asyncua. May share a cause with F-21."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP) as c:
        a = {t: await c.get_node(f"ns=1;s={t}").read_data_value() for t in ["P101.Drive.Amps", "P101.Fault", "P101.Drive.Speed", "Recipe.TargetL"]}
        await asyncio.sleep(1.0)
        b = {t: await c.get_node(f"ns=1;s={t}").read_data_value() for t in a}
        for t in a:
            same_v = a[t].Value.Value == b[t].Value.Value; same_src = a[t].SourceTimestamp == b[t].SourceTimestamp
            line = f"{t}: value {a[t].Value.Value!r} -> {b[t].Value.Value!r}; SourceTimestamp {a[t].SourceTimestamp.time()} -> {b[t].SourceTimestamp.time()}"
            if t == "P101.Drive.Speed": ok(not same_v and not same_src, line + " (changes: timestamp moves)")
            else: ok(same_v and same_src, line + " (unchanged: timestamp must not move)")
asyncio.run(main()); sys.exit(1 if F else 0)
