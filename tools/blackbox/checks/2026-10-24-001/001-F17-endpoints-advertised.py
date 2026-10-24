"""F-17: the server advertises one endpoint per name it answers to (guide: the host you
wrote, every interface address for 0.0.0.0, the machine's host name, and loopback), a
GetEndpoints asked via any of those names lists that name's URL, a session opens over it,
and a GetEndpoints for a URL the server never heard of returns the whole list rather than
nothing (Part 4 §5.4.4). Names are derived from the configured host and this machine's
interfaces. Project: evidence/typed-any (endpoint opc.tcp://0.0.0.0:4841) or any.
Oracle: asyncua + Part 4."""
import asyncio, os, sys, socket, subprocess, json
from urllib.parse import urlparse
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def endpoints_via(url, ask=None):
    c = Client(url, timeout=4)
    await c.connect_socket(); await c.send_hello(); await c.open_secure_channel()
    try:
        p = ua.GetEndpointsParameters(); p.EndpointUrl = ask or url
        eps = await c.uaclient.get_endpoints(p)
        fs = await c.uaclient.find_servers(ua.FindServersParameters(EndpointUrl=ask or url))
        return eps, [u for s in fs for u in s.DiscoveryUrls]
    finally:
        await c.close_secure_channel(); c.disconnect_socket()
async def session_via(url):
    async with Client(url, timeout=4) as c:
        return await c.get_node("ns=1;s=Level").read_value()
async def main():
    u = urlparse(EP); port = u.port; host = u.hostname
    hn = socket.gethostname()
    ips = sorted({a["local"] for i in json.loads(subprocess.check_output(["ip", "-j", "-4", "addr"])) for a in i.get("addr_info", []) if a.get("scope") == "global"})
    if host in ("localhost", "127.0.0.1"): names = ["localhost", "127.0.0.1"]          # loopback only: the host written and loopback
    elif host == "0.0.0.0": names = ["127.0.0.1", "localhost", hn] + ips             # every interface, the host name, loopback
    else: names = [host]
    names = list(dict.fromkeys(names))
    print(f"configured {EP}; host name {hn}; interface addresses {ips}; trying {names}")
    for n in names:
        url = f"opc.tcp://{n}:{port}"
        try:
            eps, disc = await endpoints_via(url)
        except Exception as e:
            ok(False, f"GetEndpoints via {url}: {e!r}"); continue
        urls = [e.EndpointUrl for e in eps]
        print(f"  via {url}: {len(eps)} endpoint(s) {urls}; FindServers discovery {disc}")
        ok(url in urls, f"GetEndpoints via {url} lists {url}")
        if host == "0.0.0.0" and f"opc.tcp://0.0.0.0:{port}" in disc: print(f"INFO  the undialable opc.tcp://0.0.0.0:{port} is itself advertised")
        try:
            v = await session_via(url); ok(v == 1.5, f"session + read via {url} -> {v}")
        except Exception as e:
            ok(False, f"session via {url}: {e!r}")
    try:
        eps, disc = await endpoints_via(f"opc.tcp://127.0.0.1:{port}", ask=f"opc.tcp://nat.example.invalid:{port}")
        urls = [e.EndpointUrl for e in eps]
        ok(len(eps) >= 1, f"GetEndpoints for an unknown URL (opc.tcp://nat.example.invalid:{port}) returns the whole list: {urls}")
        for e in eps: print(f"    {e.EndpointUrl} policy={e.SecurityPolicyUri.rsplit('#',1)[-1]} mode={e.SecurityMode.name} tokens={[t.TokenType.name for t in e.UserIdentityTokens]} level={e.SecurityLevel}")
    except Exception as e:
        ok(False, f"GetEndpoints for an unknown URL: {e!r}")
asyncio.run(main()); sys.exit(1 if F else 0)
