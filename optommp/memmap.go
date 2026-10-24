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
//	F02A0000  (expanded) analog channel write, 0x1000 per module, 0x40
//	          per channel: +0 value in EU (float)
//	F02E0000  (expanded) digital channel read & clear, 0x600 per module,
//	          0x18 per channel: +4 on-latch, +8 off-latch — reading clears
//	F0D80000  scratchpad bits (64-bit mask); F0D80400 / F0D80408 the
//	          set-on / set-off masks; F0D81000 int32 × 1024; F0D82000
//	          float × 1024
//	F0300004  power-up-clear flag (non-zero: a PUC is owed)
//	F0380000  write 1 to send the power-up clear
//
// Masks are big-endian quadlets with channel 0 the least significant bit.

const (
	addrDigitalPacked = 0xF1001900
	addrDigitalMOMO   = 0xF1001A00
	addrQualityCodes  = 0xF1002080
	addrAnalogWrite   = 0xF02A0000
	addrDigitalClear  = 0xF02E0000
	addrScratchBits   = 0xF0D80000
	addrScratchOn     = 0xF0D80400
	addrScratchInts   = 0xF0D81000
	addrScratchFloats = 0xF0D82000
	addrPUCFlag       = 0xF0300004
	addrPowerupClear  = 0xF0380000

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
)
