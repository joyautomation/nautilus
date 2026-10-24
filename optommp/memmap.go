package optommp

// memmap.go is the part of form 1465's memory map the driver polls and
// writes, beside the areas chassis.go reads once. Every address here was
// read or written on the bench GRV-EPIC-PR1 (2026-10-24).
//
//	F1000300  packed channel values, 0x80 per module: analog EU (float),
//	          digital feature value (uint) — NaN for a bad-quality analog
//	F1001900  (expanded) digital packed data, 0x10 per module: state mask,
//	          quality mask (set = bad), on-latch mask, off-latch mask. A
//	          slot that holds no digital module reads quality FFFFFFFF.
//	F1001A00  (expanded) digital packed must-on/must-off, 8 per module:
//	          on mask, off mask — set bits switch, clear bits leave alone
//	F1002080  quality indicator codes, 0x100 per module, 4 per channel
//	F0260000  (expanded) analog channel read, 0x1000 per module, 0x40 per
//	          channel: +0 value in EU (float) — what a pointer names
//	F01E0000  (expanded) digital channel read, 0x600 per module, 0x18 per
//	          channel: +0x10 feature value — what a pointer names
//	F02A0000  (expanded) analog channel write, 0x1000 per module, 0x40
//	          per channel: +0 value in EU (float)
//	F02E0000  (expanded) digital channel read & clear, 0x600 per module,
//	          0x18 per channel: +4 on-latch, +8 off-latch — reading clears
//	F0D80000  scratchpad bits (64-bit mask); F0D80400 / F0D80408 the
//	          set-on / set-off masks; F0D81000 int32 × 1024; F0D82000
//	          float × 1024
//	F0300004  power-up-clear flag (non-zero: a PUC is owed)
//	F0380000  write 1 to send the power-up clear
//	F0300054  communication watchdog time in ms (0 = off); set it at
//	          F0380010. Outputs go to their channel's watchdog value
//	          (config +0x24) where the channel enables it (+0x28)
//	F0D50000  custom configuration: 1024 pointers, each a map address
//	F0D60000  custom data access: word i reads (and writes) the address
//	          pointer i names; an unset pointer reads 0
//	F03FFFC0  streaming configuration (stored to flash): +8 start
//	          address, +C size in bytes (≤ 1480), +10 on/off, +14
//	          interval ms, +18 UDP port, +20..+3C target IPs #1..#8. A
//	          datagram is a 2-byte length (header included), a zero, 0xAn
//	          (n counts up), the 4-byte start address, then the data
//
// Masks are big-endian quadlets with channel 0 the least significant bit.

const (
	addrDigitalPacked = 0xF1001900
	addrDigitalMOMO   = 0xF1001A00
	addrQualityCodes  = 0xF1002080
	addrAnalogRead    = 0xF0260000
	addrDigitalRead   = 0xF01E0000
	addrAnalogWrite   = 0xF02A0000
	addrDigitalClear  = 0xF02E0000
	addrScratchBits   = 0xF0D80000
	addrScratchOn     = 0xF0D80400
	addrScratchInts   = 0xF0D81000
	addrScratchFloats = 0xF0D82000
	addrPUCFlag       = 0xF0300004
	addrPowerupClear  = 0xF0380000
	addrWatchdog      = 0xF0300054
	addrSetWatchdog   = 0xF0380010
	addrCustomConfig  = 0xF0D50000
	addrCustomData    = 0xF0D60000
	addrStreamConfig  = 0xF03FFFC0
	addrStreamStart   = addrStreamConfig + 0x08 // start, size
	addrStreamOn      = addrStreamConfig + 0x10
	addrStreamTiming  = addrStreamConfig + 0x14 // interval, port
	addrStreamTarget  = addrStreamConfig + 0x20
	streamConfigLen   = 0x40
	streamHeaderLen   = 8

	valuesModuleStride  = MaxChannels * 4 // 0x80
	digitalModuleStride = 0x10
	momoModuleStride    = 8
	qualityModuleStride = 0x100
	pointModuleStride   = 0x1000
	pointChannelStride  = 0x40
	clearModuleStride   = 0x600
	clearChannelStride  = 0x18
	clearOnLatch        = 4
	clearOffLatch       = 8
	digitalReadFeature  = 0x10
	cfgWatchdogEnable   = 0x28
)
