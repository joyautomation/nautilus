"""F-24: with endpoint opc.tcp://0.0.0.0:port the advertised endpoints are names a client can
dial (guide: "one per name it answers to"); opc.tcp://0.0.0.0:port itself is not one and
must not appear in GetEndpoints or FindServers. Project: evidence/typed-any. Oracle: asyncua."""
import asyncio, os, sys
from urllib.parse import urlparse
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    u = urlparse(EP)
    if u.hostname != "0.0.0.0": print(f"SKIP  configured host {u.hostname}"); return
    URL = f"opc.tcp://127.0.0.1:{u.port}"; c = Client(URL, timeout=5)
    await c.connect_socket(); await c.send_hello(); await c.open_secure_channel()
    p = ua.GetEndpointsParameters(); p.EndpointUrl = f"opc.tcp://nat.example.invalid:{u.port}"
    eps = [e.EndpointUrl for e in await c.uaclient.get_endpoints(p)]
    disc = [d for s in await c.uaclient.find_servers(ua.FindServersParameters(EndpointUrl=URL)) for d in s.DiscoveryUrls]
    await c.close_secure_channel(); c.disconnect_socket()
    print("GetEndpoints (whole list):", eps); print("FindServers discovery URLs:", disc)
    ok(EP not in eps, f"{EP} is not an advertised endpoint")
    ok(EP not in disc, f"{EP} is not a discovery URL")
asyncio.run(main()); sys.exit(1 if F else 0)
