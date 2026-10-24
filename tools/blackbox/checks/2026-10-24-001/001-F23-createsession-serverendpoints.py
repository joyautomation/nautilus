"""F-23: a client that reaches the server through an address it was not told about (a NAT
address, a DNS alias; the guide says such a client "is given the whole list to choose from")
can open a session: CreateSessionResponse.ServerEndpoints is not empty (Part 4 §5.6.2 - the
client verifies this list and refuses an endpoint it does not contain) and a standard
client connects. Dials the server through 127.0.1.1, a loopback alias no endpoint names;
needs a server bound to every interface. Project: evidence/typed-any. Oracle: asyncua + Part 4."""
import asyncio, os, sys
from urllib.parse import urlparse
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    u = urlparse(EP)
    if u.hostname != "0.0.0.0": print(f"SKIP  needs a server bound to every interface (configured host {u.hostname})"); return
    URL = f"opc.tcp://127.0.1.1:{u.port}"
    c = Client(URL, timeout=5); await c.connect_socket(); await c.send_hello(); await c.open_secure_channel()
    p = ua.GetEndpointsParameters(); p.EndpointUrl = URL
    eps = await c.uaclient.get_endpoints(p); print(f"GetEndpoints({URL}): {len(eps)} endpoint(s)")
    ok(len(eps) >= 1, "GetEndpoints for the unknown address returns the list (the guide's promise)")
    params = ua.CreateSessionParameters(); params.ClientNonce = b"\x00" * 32
    params.ClientDescription = ua.ApplicationDescription(ApplicationUri="urn:check", ProductUri="urn:check", ApplicationName=ua.LocalizedText("check"), ApplicationType=ua.ApplicationType.Client)
    params.EndpointUrl = URL; params.SessionName = "check"; params.RequestedSessionTimeout = 60000; params.MaxResponseMessageSize = 0
    r = await c.uaclient.create_session(params)
    ok(len(r.ServerEndpoints) >= 1, f"CreateSession(EndpointUrl={URL}).ServerEndpoints = {[e.EndpointUrl for e in r.ServerEndpoints]}")
    await c.close_secure_channel(); c.disconnect_socket()
    try:
        async with Client(URL, timeout=5) as c2: v = await c2.get_node("ns=1;s=Level").read_value(); ok(v == 1.5, f"asyncua session via {URL} reads Level = {v}")
    except Exception as e: ok(False, f"asyncua session via {URL}: {e!r}")
asyncio.run(main()); sys.exit(1 if F else 0)
