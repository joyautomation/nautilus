"""Pass (release: starts and restarts its own controller): a restart of `naut run` under a
subscribed client - the old client receives a StatusChangeNotification Bad_ServerHalted, the
new server accepts a session and a subscription, and the dashboard answers throughout.
Runs a copy of SUT_PROJECT (evidence/typed) on HTTP 8092 / OPC UA 4852. Oracle: asyncua."""
import asyncio, os, sys, subprocess, shutil, tempfile, time, signal, urllib.request
from asyncua import Client, ua
F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
HTTP, EP = "http://localhost:8092", "opc.tcp://localhost:4852"
def start(d):
    return subprocess.Popen(["naut", "run", "."], cwd=d, stdout=open(f"{d}/run.log", "a"), stderr=subprocess.STDOUT)
async def wait_up():
    for _ in range(50):
        try:
            async with Client(EP, timeout=2) as c: await c.get_node("ns=1;s=Level").read_value(); return True
        except Exception: await asyncio.sleep(0.2)
    return False
class H:
    def __init__(self): self.status = []; self.data = 0
    def datachange_notification(self, node, val, data): self.data += 1
    def status_change_notification(self, status): self.status.append(status)
async def main():
    d = tempfile.mkdtemp(); shutil.copytree(os.environ["SUT_PROJECT"], d, dirs_exist_ok=True)
    y = open(f"{d}/nautilus.yaml").read().replace('addr: "localhost:8080"', 'addr: "localhost:8092"').replace("opcua: {}", "opcua:\n  endpoint: opc.tcp://localhost:4852").replace("name: typed\n", "name: typedrestart\n")
    open(f"{d}/nautilus.yaml", "w").write(y)
    p1 = start(d); ok(await wait_up(), "first instance up")
    c1 = Client(EP); await c1.connect(); h = H(); sub = await c1.create_subscription(100, h)
    await sub.subscribe_data_change(c1.get_node("ns=1;s=Counter")); await asyncio.sleep(0.6)
    ok(h.data >= 3, f"subscriber receiving ({h.data} notifications)")
    t0 = time.monotonic(); p1.send_signal(signal.SIGTERM)
    for _ in range(60):
        if h.status: break
        await asyncio.sleep(0.1)
    names = [ua.StatusCode(s.Status.value if hasattr(s.Status, 'value') else s.Status).name if not isinstance(s, str) else s for s in h.status]
    ok(any(("ServerHalted" in n) or ("Shutdown" in n) for n in names), f"old client got StatusChangeNotification {names} {time.monotonic()-t0:.1f} s after SIGTERM (Bad_ServerHalted on pr-416, Bad_Shutdown on pr-416-dee0ce0; either says the server is going away)")
    try: await c1.disconnect()
    except Exception as e: print("INFO  old client disconnect:", repr(e))
    p1.wait(timeout=30); print(f"INFO  first instance exited {time.monotonic()-t0:.1f} s after SIGTERM")
    p2 = start(d); ok(await wait_up(), "second instance up")
    log = open(f"{d}/run.log").read(); ok("address already in use" not in log, "second instance bound both ports")
    async with Client(EP) as c2:
        h2 = H(); s2 = await c2.create_subscription(100, h2); await s2.subscribe_data_change(c2.get_node("ns=1;s=Counter")); await asyncio.sleep(0.6)
        ok(h2.data >= 3, f"new subscription on the restarted server receiving ({h2.data})")
    ok(urllib.request.urlopen(HTTP + "/api/state").status == 200, "dashboard/API answers after the restart")
    p2.send_signal(signal.SIGTERM); p2.wait(timeout=30); shutil.rmtree(d)
asyncio.run(main()); sys.exit(1 if F else 0)
