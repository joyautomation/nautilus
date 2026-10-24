"""F-12: SourceTimestamp is when the tag's current value was first seen - the server's
start for a value nothing has changed since (guide, Quality and timestamps) - and moves
only when the value does; ServerTimestamp is the read time. Reads setpoints the program
never writes (Str1, Time1, Flag, Small) and compares with ServerStatus.StartTime. Waits
until the server is at least 4 s old so a first-sample timestamp would be told apart.
Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys, datetime
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
TAGS = ["Str1", "Time1", "Flag", "Small", "Fixed"]
async def main():
    async with Client(EP) as c:
        start = await c.get_node("i=2257").read_value()
        now = datetime.datetime.now(datetime.timezone.utc)
        age = (now - start).total_seconds()
        print(f"StartTime={start.isoformat()} age={age:.1f}s")
        if age < 4: await asyncio.sleep(4 - age)
        first = {}
        for t in TAGS:
            dv = await c.get_node(f"ns=1;s={t}").read_data_value()
            first[t] = dv
            d = (dv.SourceTimestamp - start).total_seconds()
            ok(abs(d) <= 1.5, f"{t}: SourceTimestamp {dv.SourceTimestamp.isoformat()} is StartTime {d:+.3f}s (ServerTimestamp {dv.ServerTimestamp.isoformat()})")
            ok(abs((dv.ServerTimestamp - datetime.datetime.now(datetime.timezone.utc)).total_seconds()) < 2, f"{t}: ServerTimestamp is now")
        await asyncio.sleep(1.2)
        for t in TAGS:
            dv = await c.get_node(f"ns=1;s={t}").read_data_value()
            ok(dv.SourceTimestamp == first[t].SourceTimestamp and dv.ServerTimestamp > first[t].ServerTimestamp,
               f"{t}: 1.2 s later SourceTimestamp unchanged ({dv.SourceTimestamp == first[t].SourceTimestamp}), ServerTimestamp moved ({dv.ServerTimestamp > first[t].ServerTimestamp})")
asyncio.run(main()); sys.exit(1 if F else 0)
