"""F-25: a single OPC UA subscription requested with PublishingInterval = 0 must not crash the
controller. Part 4 §5.13.2.2: the server revises the publishing interval to a value it supports;
0 is not a usable rate and must be revised to a positive minimum. On this build the server
returns RevisedPublishingInterval=0 and the subscription goroutine then calls time.NewTicker(0),
which panics and takes down the whole `naut run` process (scan loop, OPC UA and HTTP API).

This check starts its OWN throwaway controller (so a crash harms nothing else), from a copy of
SUT_PROJECT on ports of its own, asks for a pub-0 subscription, then asserts the controller is
still alive (scan loop advancing on the HTTP API). Exit 0 = alive (fixed); 1 = crashed/wedged
(the finding). Oracle: asyncua + Part 4; the HTTP scan counter is the liveness witness.
"""
import os, sys, time, json, subprocess, tempfile, shutil, signal, urllib.request, urllib.error, re, asyncio
from asyncua import Client, ua
SRC = os.environ["SUT_PROJECT"]; HTTP_PORT = 8151; OPC_PORT = 4951
HTTP = f"http://127.0.0.1:{HTTP_PORT}"; EP = f"opc.tcp://127.0.0.1:{OPC_PORT}"; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def counter():
    try: return json.load(urllib.request.urlopen(HTTP + "/api/state", timeout=2)).get("tags", {}).get("Counter")
    except Exception as e: return f"ERR:{type(e).__name__}"
d = tempfile.mkdtemp(); proc = None
try:
    for root, _, files in os.walk(SRC):
        rel = os.path.relpath(root, SRC); dst = os.path.join(d, rel); os.makedirs(dst, exist_ok=True)
        for fn in files: shutil.copy2(os.path.join(root, fn), os.path.join(dst, fn))
    y = os.path.join(d, "nautilus.yaml"); s = open(y).read()
    s = re.sub(r'addr:\s*"[^"]*"', f'addr: "127.0.0.1:{HTTP_PORT}"', s, count=1)
    s = re.sub(r'opcua:.*', f'opcua: {{ endpoint: opc.tcp://127.0.0.1:{OPC_PORT} }}', s, count=1)
    open(y, "w").write(s)
    proc = subprocess.Popen(["naut", "run", "."], cwd=d, stdout=open(os.path.join(d, "run.log"), "w"), stderr=subprocess.STDOUT)
    for _ in range(40):
        time.sleep(0.25)
        if isinstance(counter(), int): break
    c0 = counter(); ok(isinstance(c0, int), f"own controller up, scan Counter = {c0}")
    async def sub0():
        c = Client(EP, timeout=6); await c.connect()
        p = ua.CreateSubscriptionParameters(); p.RequestedPublishingInterval = 0; p.RequestedLifetimeCount = 1000; p.RequestedMaxKeepAliveCount = 10; p.MaxNotificationsPerPublish = 0; p.PublishingEnabled = True; p.Priority = 0
        r = await c.uaclient.create_subscription(p, callback=lambda *a: None)
        print(f"   CreateSubscription(RequestedPublishingInterval=0) -> RevisedPublishingInterval={r.RevisedPublishingInterval}")
        ok(r.RevisedPublishingInterval and r.RevisedPublishingInterval > 0, f"RevisedPublishingInterval revised to a positive rate (got {r.RevisedPublishingInterval})")
        try: await asyncio.wait_for(c.disconnect(), 3)
        except Exception: pass
    try: asyncio.run(sub0())
    except Exception as e: print(f"   (client side: {type(e).__name__})")
    time.sleep(2.0)
    crashed = proc.poll() is not None
    c1 = counter(); advancing = isinstance(c0, int) and isinstance(c1, int) and c1 != c0
    print(f"   process exited={crashed}; scan Counter {c0} -> {c1}")
    ok(not crashed, "the controller process is still running after a pub-0 subscription")
    ok(advancing, f"the scan loop and HTTP API are still alive (Counter advancing: {c0} -> {c1})")
    if crashed:
        tail = open(os.path.join(d, "run.log")).read().splitlines()
        print("   run.log tail:", " | ".join(tail[-6:]))
finally:
    if proc and proc.poll() is None:
        proc.send_signal(signal.SIGKILL); proc.wait()
    shutil.rmtree(d, ignore_errors=True)
sys.exit(1 if F else 0)
