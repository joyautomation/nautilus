"""F-30: the server advertises Server.ServerCapabilities.OperationLimits.MaxNodesPerRead (i=11705)
= 32, but a Read of more than that many nodes succeeds for all of them. Part 4 §5.10.2 / Part 5
§6.3.31: when a request exceeds an advertised operation limit the server shall return
Bad_TooManyOperations; advertising a limit it does not enforce misleads a conforming client that
chunks its reads to fit. Project: SUT_PROJECT. Oracle: asyncua + Part 4/5."""
import os, sys, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP, timeout=8) as c:
        lim = (await c.uaclient.read_attributes([ua.NodeId(11705)], ua.AttributeIds.Value))[0].Value.Value
        print(f"   advertised MaxNodesPerRead = {lim}")
        ok(isinstance(lim, int) and lim > 0, f"MaxNodesPerRead is advertised ({lim})")
        folder = [k for k in await c.get_node(85).get_children() if k.nodeid.NamespaceIndex == 1][0]
        allv = []
        async def collect(nd):
            for k in await nd.get_children():
                if await k.read_node_class() == ua.NodeClass.Variable: allv.append(k.nodeid)
                else: await collect(k)
        await collect(folder)
        over = lim + 1; ids = (allv * (over // len(allv) + 1))[:over]
        rp = ua.ReadParameters(); rp.NodesToRead = [ua.ReadValueId(NodeId=i, AttributeId=ua.AttributeIds.Value) for i in ids]
        service_err = None
        try: r = await c.uaclient.read(rp)
        except Exception as e: service_err = type(e).__name__; r = []
        if service_err: print(f"   Read of {over} nodes -> service fault {service_err}"); ok("TooManyOperations" in service_err, f"Read over the limit refused with Bad_TooManyOperations (got {service_err})")
        else:
            good = sum(1 for x in r if x.StatusCode.is_good()); print(f"   Read of {over} nodes (limit {lim}) -> {len(r)} results, {good} good")
            ok(False, f"Read of {over} nodes (over the advertised limit {lim}) was accepted and returned {good} good results instead of Bad_TooManyOperations")
asyncio.run(main()); sys.exit(1 if F else 0)
