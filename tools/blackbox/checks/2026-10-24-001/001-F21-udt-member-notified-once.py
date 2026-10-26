"""F-21: a monitored item on a UDT member whose value does not change is not notified when a
sibling member changes. Part 4 §7.17.2 DataChangeFilter: a notification is generated when
the value or status changes; the guide: the server "notifies only the monitored items that
cover a changed tag". The program writes P101.Drive.Speed and P101.Starts every scan and
never P101.Drive.Amps or P101.Fault. Project: evidence/typed. Oracle: asyncua + Part 4."""
import asyncio, os, sys, time
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
class H:
    def __init__(self): self.ev = []
    def datachange_notification(self, node, val, data): self.ev.append((node.nodeid.Identifier, val))
async def main():
    async with Client(EP) as c:
        h = H(); sub = await c.create_subscription(50, h)
        await sub.subscribe_data_change([c.get_node(f"ns=1;s={t}") for t in ["P101.Drive.Amps", "P101.Fault", "P101.Drive.Speed", "Fixed"]])
        await asyncio.sleep(0.5); h.ev.clear(); await asyncio.sleep(2.0)
        n = {t: [v for (i, v) in h.ev if i == t] for t in ["P101.Drive.Amps", "P101.Fault", "P101.Drive.Speed", "Fixed"]}
        print({t: (len(v), sorted({str(x) for x in v})[:4]) for t, v in n.items()})
        ok(len(n["P101.Drive.Speed"]) >= 15, f"P101.Drive.Speed (changes every scan): {len(n['P101.Drive.Speed'])} notifications in 2 s")
        ok(len(n["Fixed"]) == 0, f"Fixed (scalar rewritten with the same value): {len(n['Fixed'])} notifications")
        ok(len(n["P101.Drive.Amps"]) == 0, f"P101.Drive.Amps (member never written, sibling changes): {len(n['P101.Drive.Amps'])} notifications, all value {set(n['P101.Drive.Amps'])}")
        ok(len(n["P101.Fault"]) == 0, f"P101.Fault (member never written, sibling changes): {len(n['P101.Fault'])} notifications")
        await sub.delete()
asyncio.run(main()); sys.exit(1 if F else 0)
