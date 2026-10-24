"""F-02: with the default endpoint (host `localhost`) the server listens on 127.0.0.1, as
the guide says (`localhost` listens on `127.0.0.1`): a session over opc.tcp://127.0.0.1:port
opens and reads a tag. Also reports whether [::1] is served. Skips (exit 0) when the
configured host is not localhost. Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys
from urllib.parse import urlparse
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def try_read(url):
    try:
        async with Client(url, timeout=4) as c:
            return (await c.get_node("ns=1;s=Level").read_value())
    except Exception as e:
        return e
async def main():
    u = urlparse(EP)
    if u.hostname != "localhost":
        print(f"SKIP  configured host is {u.hostname!r}, not localhost"); return
    r = await try_read(f"opc.tcp://127.0.0.1:{u.port}")
    ok(r == 1.5, f"session + read via opc.tcp://127.0.0.1:{u.port} -> {r!r}")
    r6 = await try_read(f"opc.tcp://[::1]:{u.port}")
    print(f"INFO  session + read via opc.tcp://[::1]:{u.port} -> {r6!r}")
asyncio.run(main()); sys.exit(1 if F else 0)
