"""Pass: the server offers the None security policy with anonymous sessions and nothing
else that needs a certificate (guide, Security). Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    eps = await Client(EP).connect_and_get_server_endpoints()
    ok(len(eps) >= 1, f"{len(eps)} endpoint(s)")
    for e in eps:
        toks = [(t.TokenType.name, t.PolicyId) for t in e.UserIdentityTokens]
        print(f"{e.EndpointUrl} policy={e.SecurityPolicyUri} mode={e.SecurityMode.name} tokens={toks} cert={len(e.ServerCertificate or b'')}B")
        ok(e.SecurityPolicyUri.endswith("#None") and e.SecurityMode == ua.MessageSecurityMode.None_, f"{e.EndpointUrl}: policy None / mode None")
        ok(all(t.TokenType == ua.UserTokenType.Anonymous for t in e.UserIdentityTokens) and e.UserIdentityTokens, f"{e.EndpointUrl}: anonymous token only")
asyncio.run(main()); sys.exit(1 if F else 0)
