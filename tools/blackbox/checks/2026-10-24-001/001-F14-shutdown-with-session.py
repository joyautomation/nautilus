"""F-14 (filed nautilus#423): `naut run` exits promptly on SIGTERM whether or not an OPC UA
session is open (a customer expects Ctrl+C and `naut run` again to work with a SCADA client
attached; the second start must not lose the HTTP port). Runs its own copy of SUT_PROJECT on
HTTP 8095 / OPC UA 4855. release. Oracle: process exit timing; asyncua for the session."""
import asyncio, os, sys, subprocess, shutil, tempfile, time, signal
from asyncua import Client, ua
F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
EP = "opc.tcp://localhost:4855"
def start(d): return subprocess.Popen(["naut", "run", "."], cwd=d, stdout=open(f"{d}/run.log", "a"), stderr=subprocess.STDOUT)
async def wait_up():
    for _ in range(50):
        try:
            async with Client(EP, timeout=2) as c: await c.get_node("ns=1;s=Level").read_value(); return True
        except Exception: await asyncio.sleep(0.2)
    return False
class H:
    def datachange_notification(self, node, val, data): pass
    def status_change_notification(self, status): pass
async def stop_time(p):
    t0 = time.monotonic(); p.send_signal(signal.SIGTERM); p.wait(timeout=30); return time.monotonic() - t0
async def main():
    d = tempfile.mkdtemp(); shutil.copytree(os.environ["SUT_PROJECT"], d, dirs_exist_ok=True)
    y = open(f"{d}/nautilus.yaml").read().replace('addr: "localhost:8080"', 'addr: "localhost:8095"').replace("opcua: {}", "opcua:\n  endpoint: opc.tcp://localhost:4855").replace("name: typed\n", "name: typedstop\n")
    open(f"{d}/nautilus.yaml", "w").write(y)
    p = start(d); assert await wait_up(); t = await stop_time(p); ok(t < 2, f"no client: exit {t:.2f} s after SIGTERM")
    p = start(d); assert await wait_up(); c = Client(EP); await c.connect(); t = await stop_time(p); ok(t < 2, f"one session, no subscription: exit {t:.2f} s after SIGTERM")
    try: await c.disconnect()
    except Exception: pass
    p = start(d); assert await wait_up(); c = Client(EP); await c.connect(); s = await c.create_subscription(100, H()); await s.subscribe_data_change(c.get_node("ns=1;s=Counter")); await asyncio.sleep(0.3)
    t = await stop_time(p); ok(t < 2, f"one subscribed session: exit {t:.2f} s after SIGTERM")
    try: await c.disconnect()
    except Exception: pass
    shutil.rmtree(d)
asyncio.run(main()); sys.exit(1 if F else 0)
