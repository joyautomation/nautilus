"""Pass (release: watches 3 s of scans): a program-written tag that changes every 100 ms
scan (Counter) is notified once per change with no value skipped, gaps of about one sweep,
and a distinct SourceTimestamp per change; a tag rewritten with the same value every scan
(Fixed := 7) produces no notification after the initial one. Project: evidence/typed.
Oracle: asyncua."""
import asyncio, os, sys, time
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
class H:
    def __init__(self): self.ev = []
    def datachange_notification(self, node, val, data): self.ev.append((time.monotonic(), node.nodeid.Identifier, val, data.monitored_item.Value.SourceTimestamp, data.monitored_item.Value.StatusCode))
async def main():
    async with Client(EP) as c:
        h = H(); sub = await c.create_subscription(50, h)
        await sub.subscribe_data_change([c.get_node("ns=1;s=Counter"), c.get_node("ns=1;s=Fixed"), c.get_node("ns=1;s=Blink")])
        await asyncio.sleep(0.5); h.ev.clear()
        await asyncio.sleep(3.0)
        cnt = [e for e in h.ev if e[1] == "Counter"]; fx = [e for e in h.ev if e[1] == "Fixed"]; bl = [e for e in h.ev if e[1] == "Blink"]
        gaps = [(b[0] - a[0]) * 1000 for a, b in zip(cnt, cnt[1:])]
        vals = [e[2] for e in cnt]; skipped = sum(1 for a, b in zip(vals, vals[1:]) if b != a + 1)
        print(f"Counter: {len(cnt)} notifications in 3 s; gaps min/avg/max {min(gaps):.0f}/{sum(gaps)/len(gaps):.0f}/{max(gaps):.0f} ms; values {vals[0]}..{vals[-1]}")
        ok(25 <= len(cnt) <= 33, "Counter: about one notification per 100 ms scan")
        ok(skipped == 0, f"Counter: no value skipped ({skipped} skips)")
        ok(len({e[3] for e in cnt}) == len(cnt), "Counter: a distinct SourceTimestamp per change")
        ok(all(e[4].is_good() for e in cnt), "Counter: every notification Good")
        ok(max(gaps) < 250, "Counter: no gap longer than 250 ms")
        ok(len(fx) == 0, f"Fixed (rewritten with the same value every scan): {len(fx)} notifications after the initial")
        print(f"INFO  Blink (TON-driven toggle): {len(bl)} notifications, values {[e[2] for e in bl][:8]}")
        await sub.delete()
asyncio.run(main()); sys.exit(1 if F else 0)
