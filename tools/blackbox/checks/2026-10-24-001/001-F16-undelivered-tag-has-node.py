"""F-16 (filed nautilus#420): every tag in the project is a node, including a driver input the
device has never delivered; a tag whose source reports it untrustworthy reads
Bad_DeviceFailure and one never delivered reads Bad_NotConnected (guide's quality table).
This check covers the half that needs no restart: TC_A_Tx (a register the slave answers
with exception 2) must have a node and read Bad_DeviceFailure. The Bad_NotConnected half
needs a controller started with no device and is recorded in the run's evidence by hand.
Starts a pymodbus slave on SUT_MODBUS. Project: evidence/mb. Oracle: asyncua + Part 3;
pymodbus as the device; GET /api/state for the quality word nautilus itself uses."""
import asyncio, os, sys, subprocess, time, json, urllib.request
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; MB = os.environ["SUT_MODBUS"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
host, port = MB.rsplit(":", 1)
SLAVE = f"""
import asyncio
from pymodbus.server import StartAsyncTcpServer
from pymodbus.datastore import ModbusSequentialDataBlock, ModbusDeviceContext, ModbusServerContext
dev = ModbusDeviceContext(di=ModbusSequentialDataBlock(1, [0]*12+[1]+[0]*87), hr=ModbusSequentialDataBlock(1, [1234]+[0]*99), co=ModbusSequentialDataBlock(1, [0]*100), ir=ModbusSequentialDataBlock(1, [0]*100))
asyncio.run(StartAsyncTcpServer(context=ModbusServerContext(devices={{1: dev}}, single=False), address=({host!r}, {int(port)})))
"""
async def main():
    s = subprocess.Popen([sys.executable, "-I", "-c", SLAVE], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    async with Client(EP) as c:
        for _ in range(300):   # the driver's reconnect backoff reaches 60 s
            dv = await c.get_node("ns=1;s=TC_A_Tpv").read_data_value(raise_on_bad_status=False)
            if dv.StatusCode.is_good(): break
            await asyncio.sleep(0.25)
        ok(dv.StatusCode.is_good(), f"device up: TC_A_Tpv {dv.StatusCode.name}")
        await asyncio.sleep(2)
        q = json.load(urllib.request.urlopen(HTTP + "/api/state")).get("quality", {}); print("HTTP /api/state quality:", q)
        folder = [n for n in await c.get_objects_node().get_children() if n.nodeid.NamespaceIndex != 0][0]
        names = sorted([(await k.read_browse_name()).Name for k in await folder.get_children()]); print("folder children:", names)
        ok("TC_A_Tx" in names, "TC_A_Tx (never delivered: the slave refuses the register) has a node")
        dv = await c.get_node("ns=1;s=TC_A_Tx").read_data_value(raise_on_bad_status=False)
        ok(dv.StatusCode.name == "BadDeviceFailure", f"read TC_A_Tx: {dv.StatusCode.name} (HTTP quality {q.get('TC_A_Tx')!r}; guide: bad -> Bad_DeviceFailure)")
    s.kill(); s.wait()
asyncio.run(main()); sys.exit(1 if F else 0)
