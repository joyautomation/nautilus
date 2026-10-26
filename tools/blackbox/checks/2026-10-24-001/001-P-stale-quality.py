"""Pass (release: drives a Modbus slave up and down): a driver input is Good with the device's
value while the device answers, Uncertain_LastUsableValue with the last value held when the
device stops, and Good again when it returns, with SourceTimestamp unchanged while the value
did not change (guide, Quality and timestamps). Starts a pymodbus slave on SUT_MODBUS
(holding[0]=1234 -> TC_A_Tpv 123.4 with scale 0.1, discrete[12]=1 -> TC_A_AlmH), stops it,
restarts it. The driver's reconnect backoff reaches 60 s after a long outage, so "device up"
is given 75 s. Project: evidence/mb. Oracle: asyncua; pymodbus as the device."""
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
def slave(): return subprocess.Popen([sys.executable, "-I", "-c", SLAVE], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
async def wait_for(c, nid, pred, secs):
    t0 = time.monotonic(); last = None
    while time.monotonic() - t0 < secs:
        dv = await c.get_node(nid).read_data_value(raise_on_bad_status=False); last = dv
        if pred(dv): return dv, time.monotonic() - t0
        await asyncio.sleep(0.25)
    return last, None
def show(dv): return f"{dv.StatusCode.name} value={dv.Value.Value if dv.Value else None} src={dv.SourceTimestamp.isoformat(timespec='milliseconds') if dv.SourceTimestamp else None}"
async def main():
    s = slave(); print("slave started on", MB)
    async with Client(EP) as c:
        dv, t = await wait_for(c, "ns=1;s=TC_A_Tpv", lambda d: d.StatusCode.is_good() and d.Value.Value == 123.4, 75)
        ok(t is not None, f"device up: TC_A_Tpv {show(dv)} after {t}s")
        alm = await c.get_node("ns=1;s=TC_A_AlmH").read_data_value(raise_on_bad_status=False); ok(alm.StatusCode.is_good() and alm.Value.Value is True, f"device up: TC_A_AlmH {show(alm)}")
        src_good = dv.SourceTimestamp
        s.kill(); s.wait(); print("slave killed")
        dv, t = await wait_for(c, "ns=1;s=TC_A_Tpv", lambda d: d.StatusCode.name == "UncertainLastUsableValue", 45)
        ok(t is not None and dv.Value.Value == 123.4, f"device down: TC_A_Tpv {show(dv)} after {t}s (value held)")
        ok(dv.SourceTimestamp == src_good, "device down: SourceTimestamp unchanged (value did not change)")
        s = slave(); print("slave restarted")
        dv, t = await wait_for(c, "ns=1;s=TC_A_Tpv", lambda d: d.StatusCode.is_good(), 75)
        ok(t is not None and dv.Value.Value == 123.4, f"device back: TC_A_Tpv {show(dv)} after {t}s")
        ok(dv.SourceTimestamp == src_good, "device back: SourceTimestamp still unchanged")
    s.kill(); s.wait()
asyncio.run(main()); sys.exit(1 if F else 0)
