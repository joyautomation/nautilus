"""F-15 (filed nautilus#419): a change of quality with the value unchanged is pushed to a
subscriber - the default DataChangeFilter trigger is StatusValue (Part 4 §7.17.2). Drives
a pymodbus slave on SUT_MODBUS up, subscribes TC_A_Tpv with an explicit StatusValue
trigger, kills the slave, and expects a notification carrying Uncertain_LastUsableValue.
release. Project: evidence/mb. Oracle: asyncua + Part 4; pymodbus as the device."""
import asyncio, os, sys, subprocess, time
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; MB = os.environ["SUT_MODBUS"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
host, port = MB.rsplit(":", 1)
SLAVE = f"""
import asyncio
from pymodbus.server import StartAsyncTcpServer
from pymodbus.datastore import ModbusSequentialDataBlock, ModbusDeviceContext, ModbusServerContext
dev = ModbusDeviceContext(di=ModbusSequentialDataBlock(1, [0]*12+[1]+[0]*87), hr=ModbusSequentialDataBlock(1, [1234]+[0]*99), co=ModbusSequentialDataBlock(1, [0]*100), ir=ModbusSequentialDataBlock(1, [0]*100))
asyncio.run(StartAsyncTcpServer(context=ModbusServerContext(devices={{1: dev}}, single=False), address=({host!r}, {int(port)})))
"""
class H:
    def __init__(self): self.ev = []
    def datachange_notification(self, node, val, data): self.ev.append((time.monotonic(), data.monitored_item.Value.StatusCode.name, val))
async def main():
    s = subprocess.Popen([sys.executable, "-I", "-c", SLAVE], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    async with Client(EP) as c:
        n = c.get_node("ns=1;s=TC_A_Tpv")
        for _ in range(300):   # the driver's reconnect backoff reaches 60 s
            dv = await n.read_data_value(raise_on_bad_status=False)
            if dv.StatusCode.is_good() and dv.Value and dv.Value.Value == 123.4: break
            await asyncio.sleep(0.25)
        ok(dv.StatusCode.is_good(), f"device up: Read {dv.StatusCode.name} {dv.Value.Value if dv.Value else None}")
        h = H(); sub = await c.create_subscription(100, h)
        p = ua.CreateMonitoredItemsParameters(); p.SubscriptionId = sub.subscription_id; p.TimestampsToReturn = ua.TimestampsToReturn.Both
        r = ua.MonitoredItemCreateRequest(); r.ItemToMonitor = ua.ReadValueId(NodeId=n.nodeid, AttributeId=ua.AttributeIds.Value); r.MonitoringMode = ua.MonitoringMode.Reporting
        f = ua.DataChangeFilter(); f.Trigger = ua.DataChangeTrigger.StatusValue; f.DeadbandType = 0; f.DeadbandValue = 0.0
        r.RequestedParameters = ua.MonitoringParameters(ClientHandle=1, SamplingInterval=100, QueueSize=10, DiscardOldest=True, Filter=f)
        p.ItemsToCreate = [r]; res = (await c.uaclient.create_monitored_items(p))[0]
        print(f"CreateMonitoredItems with StatusValue trigger: {res.StatusCode.name} filter result {res.FilterResult}")
        await asyncio.sleep(1.0); print("initial notifications:", h.ev); h.ev.clear()
        s.kill(); s.wait(); t0 = time.monotonic(); print("slave killed")
        for _ in range(180):
            if any(e[1] != "Good" for e in h.ev): break
            await asyncio.sleep(0.25)
        dv = await n.read_data_value(raise_on_bad_status=False)
        print(f"Read now: {dv.StatusCode.name} {dv.Value.Value if dv.Value else None}; notifications since kill: {[(round(e[0]-t0,1), e[1], e[2]) for e in h.ev]}")
        ok(any(e[1] == "UncertainLastUsableValue" for e in h.ev), "a notification carried the Good -> Uncertain_LastUsableValue transition")
        await sub.delete()
    s.kill(); s.wait()
asyncio.run(main()); sys.exit(1 if F else 0)
