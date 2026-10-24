"""A foreign Modbus slave (pymodbus): unit 1, holding 0..99 = 123 at 0, discrete 0..99 = 1 at 12. Address 500 is NOT served, so a read there gets an exception response."""
import asyncio
from pymodbus.server import StartAsyncTcpServer
from pymodbus.datastore import ModbusSequentialDataBlock, ModbusDeviceContext, ModbusServerContext
async def main():
    dev=ModbusDeviceContext(di=ModbusSequentialDataBlock(1,[0]*12+[1]+[0]*87), hr=ModbusSequentialDataBlock(1,[1234]+[0]*99), co=ModbusSequentialDataBlock(1,[0]*100), ir=ModbusSequentialDataBlock(1,[0]*100))
    ctx=ModbusServerContext(devices={1:dev}, single=False)
    await StartAsyncTcpServer(context=ctx, address=("127.0.0.1",5020))
asyncio.run(main())
