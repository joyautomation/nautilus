package optommp

// modules.go is the groov I/O half of form 1465's "groov I/O Module Types
// and Channel Types" table, as data: a module type code → part number and
// channel count, a channel type code → what the channel measures. The
// processor reports both codes per channel in the channel-configuration
// area (F0100000), so a chassis scan needs nothing but these two maps to
// name every point. Serial and CAN modules (0x0C… / 0x08…) are listed so
// browse can name them, but they carry no I/O channel the Modbus map can
// bind, and the generator skips them.
//
// The top byte of a type code is the channel's family; Kind reads it:
// 0x50/0x54 digital in (plain / "simple"), 0x90/0x94 digital out,
// 0x60 analog in, 0xA0 analog out. Mixed modules (GRV-MM1001-10,
// F000002B) have no family of their own — each channel's own type decides.

import "fmt"

// Kind is what a channel is for, as the Modbus map and a tag file see it.
type Kind string

const (
	KindNone  Kind = ""      // unconfigured or absent channel
	KindDI    Kind = "DI"    // discrete input
	KindDO    Kind = "DO"    // discrete output
	KindAI    Kind = "AI"    // analog input (engineering units, float)
	KindAO    Kind = "AO"    // analog output (engineering units, float)
	KindComm  Kind = "comm"  // serial / CAN — no process channel
	KindOther Kind = "other" // a family this table does not know
)

// KindOf classifies a channel type code by its family byte.
func KindOf(channelType uint32) Kind {
	switch channelType >> 24 {
	case 0x00:
		return KindNone
	case 0x50, 0x54:
		return KindDI
	case 0x90, 0x94:
		return KindDO
	case 0x60:
		return KindAI
	case 0xA0:
		return KindAO
	case 0x08, 0x0C:
		return KindComm
	}
	return KindOther
}

// ModuleInfo describes one module type.
type ModuleInfo struct {
	Part     string // Opto 22 part number, "GRV-IDCI-12"
	Channels int    // channels the module carries
}

// ChannelInfo describes one channel type: the range it is configured for.
type ChannelInfo struct {
	Desc string  // "4-20 mA", "Type K Thermocouple (°C)", "Digital Input"
	Unit string  // engineering unit, "" for digital
	Lo   float64 // configured range, 0,0 when the table has none
	Hi   float64
}

// LookupModule returns the table row for a module type code. ok is false
// for a code the table does not know (a module newer than the table — the
// scan still reports it by code).
func LookupModule(code uint32) (ModuleInfo, bool) {
	m, ok := modules[code]
	return m, ok
}

// LookupChannel returns the table row for a channel type code.
func LookupChannel(code uint32) (ChannelInfo, bool) {
	c, ok := channels[code]
	return c, ok
}

// ModuleName is the part number, or the hex code for an unknown module.
func ModuleName(code uint32) string {
	if m, ok := modules[code]; ok {
		return m.Part
	}
	return fmt.Sprintf("module 0x%08X", code)
}

var modules = map[uint32]ModuleInfo{
	0x0C00001E: {"GRV-CSERI-4", 4},
	0x0C000027: {"GRV-CCANI-2", 2},
	0x50000005: {"GRV-IACDCTTL-24", 24},
	0x5000000C: {"GRV-IACI-12", 12},
	0x5000000D: {"GRV-IACIHV-12", 12},
	0x5000000E: {"GRV-IDCI-12", 12},
	0x5000000F: {"GRV-IAC-24", 24},
	0x50000010: {"GRV-IACHV-24", 24},
	0x50000011: {"GRV-IDC-24", 24},
	0x5000001A: {"GRV-IDCIFQ-12", 12},
	0x50000023: {"GRV-IDCSW-12", 12},
	0x54000007: {"GRV-IACDCTTLS-24", 24},
	0x54000008: {"GRV-IACHVS-24", 24},
	0x54000009: {"GRV-IACIHVS-12", 12},
	0x5400000A: {"GRV-IACIS-12", 12},
	0x5400000B: {"GRV-IDCIS-12", 12},
	0x54000016: {"GRV-IDCS-24", 24},
	0x5400001D: {"GRV-IACS-24", 24},
	0x60000001: {"GRV-IMA-24", 24},
	0x60000002: {"GRV-IV-24", 24},
	0x60000006: {"GRV-ITMI-8", 8},
	0x6000001F: {"GRV-IICTD-12", 12},
	0x60000020: {"GRV-IMAI-8", 8},
	0x60000021: {"GRV-ITR-12", 12},
	0x60000024: {"GRV-IRTD-8", 8},
	0x60000025: {"GRV-IVI-12", 12},
	0x60000026: {"GRV-ITM-12", 12},
	0x60000028: {"GRV-IVIRMS-10", 10},
	0x6000002A: {"GRV-IVAPM-3", 64},
	0x90000012: {"GRV-OAC-12", 12},
	0x90000013: {"GRV-ODCSRC-24", 24},
	0x90000014: {"GRV-OACI-12", 12},
	0x90000015: {"GRV-ODCI-12", 12},
	0x9000002E: {"GRV-ODCIHC-12", 12},
	0x9000002F: {"GRV-ODCIHV-12", 12},
	0x94000017: {"GRV-OACIS-12", 12},
	0x94000018: {"GRV-ODCIS-12", 12},
	0x94000019: {"GRV-OMRIS-8", 8},
	0x9400001C: {"GRV-OACS-12", 12},
	0xA0000003: {"GRV-OVMALC-8", 8},
	0xA0000004: {"GRV-OVMAILP-8", 8},
	0xF000002B: {"GRV-MM1001-10", 10},
}

var channels = map[uint32]ChannelInfo{
	0x50000000: {"Digital Input", "", 0, 0},
	0x5000002F: {"Digital Input with Quadrature", "", 0, 0},
	0x50000030: {"Digital Input with Quadrature Index", "", 0, 0},
	0x50000068: {"Switch Input, Powered", "", 0, 0},
	0x50000079: {"5-30 VDC", "VDC", 5, 30},
	0x500000A2: {"10-25 VAC", "", 0, 0},
	0x54000000: {"Simple Digital Input", "", 0, 0},
	0x60000001: {"±1.25 V", "V", -1.25, 1.25},
	0x60000002: {"±1200 mV", "mV", -1200, 1200},
	0x60000003: {"±600 mV", "mV", -600, 600},
	0x60000004: {"±300 mV", "mV", -300, 300},
	0x60000005: {"±150 mV", "mV", -150, 150},
	0x60000006: {"±75 mV", "mV", -75, 75},
	0x60000007: {"±50 mV", "mV", -50, 50},
	0x60000008: {"±25 mV", "mV", -25, 25},
	0x60000009: {"Type B Thermocouple", "°C", 0, 1820},
	0x6000000A: {"±20 V", "V", -20, 20},
	0x6000000B: {"±40 V", "V", -40, 40},
	0x6000000C: {"Type E Thermocouple", "°C", -270, 1000},
	0x6000000D: {"±80 V", "V", -80, 80},
	0x6000000E: {"Type J Thermocouple", "°C", -210, 1200},
	0x6000000F: {"Type K Thermocouple", "°C", -270, 1372},
	0x60000010: {"Type N Thermocouple", "°C", -270, 1300},
	0x60000011: {"Type R Thermocouple", "°C", -50, 1768},
	0x60000012: {"Type S Thermocouple", "°C", -50, 1768},
	0x60000013: {"Type T Thermocouple", "°C", -270, 400},
	0x60000014: {"±20 mA", "mA", -20, 20},
	0x60000015: {"0-20 mA", "mA", 0, 20},
	0x60000016: {"4-20 mA", "mA", 4, 20},
	0x60000017: {"±10 V", "V", -10, 10},
	0x60000018: {"0-10 V", "V", 0, 10},
	0x60000019: {"±5 V", "V", -5, 5},
	0x60000024: {"±160 V", "V", -160, 160},
	0x60000025: {"±2.5 V", "V", -2.5, 2.5},
	0x60000031: {"ICTD Temperature Probe", "°C", -270, 150},
	0x60000032: {"0-400k Ohms (autorange)", "Ohms", 0, 400000},
	0x60000033: {"0-400k Ohms", "Ohms", 0, 400000},
	0x60000034: {"0-200k Ohms", "Ohms", 0, 200000},
	0x60000035: {"0-100k Ohms", "Ohms", 0, 100000},
	0x60000036: {"0-50k Ohms", "Ohms", 0, 50000},
	0x60000037: {"0-40k Ohms", "Ohms", 0, 40000},
	0x60000038: {"0-20k Ohms", "Ohms", 0, 20000},
	0x60000039: {"0-10k Ohms", "Ohms", 0, 10000},
	0x6000003A: {"0-5k Ohms", "Ohms", 0, 5000},
	0x6000003B: {"0-4k Ohms", "Ohms", 0, 4000},
	0x6000003C: {"0-2k Ohms", "Ohms", 0, 2000},
	0x6000003D: {"0-1k Ohms", "Ohms", 0, 1000},
	0x6000003E: {"0-500 Ohms", "Ohms", 0, 500},
	0x6000003F: {"Thermistor: 2252 Curve", "°C", -40, 150},
	0x60000040: {"Thermistor: 3K Curve", "°C", -40, 150},
	0x60000041: {"Thermistor: 10K Type 2 Curve", "°C", -40, 150},
	0x60000042: {"Thermistor: 10K Type 3 Curve", "°C", -40, 150},
	0x60000043: {"Thermistor: Custom Curve", "°C", -40, 150},
	0x60000044: {"RTD: 1000 Ohm @ 70F Ni 3-wire", "°C", -45, 250},
	0x60000045: {"RTD: 1000 Ohm @ 0C Ni 3-wire", "°C", -60, 250},
	0x60000046: {"RTD: 1000 Ohm Pt 3-wire", "°C", -200, 850},
	0x60000047: {"RTD: 120 Ohm Ni 3-wire", "°C", -80, 260},
	0x60000048: {"RTD: 100 Ohm Ni 3-wire", "°C", -60, 250},
	0x60000049: {"RTD: 100 Ohm Pt 3-wire", "°C", -200, 850},
	0x6000004A: {"RTD: 10 Ohm Cu 3-wire", "°C", -200, 260},
	0x6000004B: {"0-8k Ohms (autorange)", "Ohms", 0, 8000},
	0x6000004C: {"0-4k Ohms (autorange)", "Ohms", 0, 4000},
	0x6000004D: {"0-2k Ohms (autorange)", "Ohms", 0, 2000},
	0x6000004E: {"0-1k Ohms (autorange)", "Ohms", 0, 1000},
	0x6000004F: {"0-800 Ohms (autorange)", "Ohms", 0, 800},
	0x60000050: {"0-400 Ohms (autorange)", "Ohms", 0, 400},
	0x60000051: {"0-200 Ohms (autorange)", "Ohms", 0, 200},
	0x60000052: {"0-100 Ohms (autorange)", "Ohms", 0, 100},
	0x60000053: {"0-80 Ohms (autorange)", "Ohms", 0, 80},
	0x60000054: {"0-40 Ohms (autorange)", "Ohms", 0, 40},
	0x60000055: {"0-20 Ohms (autorange)", "Ohms", 0, 20},
	0x60000056: {"0-8k Ohms", "Ohms", 0, 8000},
	0x60000057: {"0-800 Ohms", "Ohms", 0, 800},
	0x60000058: {"0-400 Ohms", "Ohms", 0, 400},
	0x60000059: {"0-200 Ohms", "Ohms", 0, 200},
	0x6000005A: {"0-100 Ohms", "Ohms", 0, 100},
	0x6000005B: {"0-80 Ohms", "Ohms", 0, 80},
	0x6000005C: {"0-40 Ohms", "Ohms", 0, 40},
	0x6000005D: {"0-20 Ohms", "Ohms", 0, 20},
	0x6000005E: {"0-10 Ohms", "Ohms", 0, 10},
	0x6000007C: {"0-300 Vrms", "Vrms", 0, 300},
	0x6000007D: {"0-150 Vrms", "Vrms", 0, 150},
	0x6000007E: {"0-60 Vrms", "Vrms", 0, 60},
	0x6000007F: {"0-30 Vrms", "Vrms", 0, 30},
	0x60000080: {"0-20 Vrms", "Vrms", 0, 20},
	0x60000081: {"0-10 Vrms", "Vrms", 0, 10},
	0x60000082: {"0-5 Vrms", "Vrms", 0, 5},
	0x60000083: {"0-1 Vrms", "Vrms", 0, 1},
	0x60000084: {"0-0.5 Vrms", "Vrms", 0, 0.5},
	0x60000087: {"0-600 Vrms Delta", "Vrms", 0, 600},
	0x60000088: {"0-520 Vrms Delta", "Vrms", 0, 520},
	0x60000089: {"0-400 Vrms Wye", "Vrms", 0, 400},
	0x6000008A: {"0-260 Vrms Delta", "Vrms", 0, 260},
	0x6000008B: {"0-150 Vrms Wye", "Vrms", 0, 150},
	0x6000008C: {"0-5 Arms CT", "Arms", 0, 0},
	0x6000008D: {"0-0.333 Vrms CT", "Arms", 0, 0},
	0x6000008E: {"0-300 Vrms Wye", "Vrms", 0, 300},
	0x6000008F: {"True Power", "W", 0, 0},
	0x60000090: {"Reactive Power", "VAr", 0, 0},
	0x60000091: {"Apparent Power", "VA", 0, 0},
	0x60000092: {"Power Factor", "", -1, 1},
	0x60000093: {"Peak Voltage", "V", 0, 0},
	0x60000094: {"Peak Current", "A", 0, 0},
	0x60000095: {"Frequency", "Hz", 0, 0},
	0x60000096: {"True Power At Fundamental Freq", "W", 0, 0},
	0x60000097: {"Harmonic True Power", "W", 0, 0},
	0x60000098: {"Reactive Power At Fundamental Freq", "VAr", 0, 0},
	0x60000099: {"Average Reactive Power", "VAr", 0, 0},
	0x6000009A: {"Net Energy", "kWh", 0, 0},
	0x6000009B: {"Total Unsigned Energy", "kWh", 0, 0},
	0x6000009C: {"Positive Energy", "kWh", 0, 0},
	0x6000009D: {"Negative Energy", "kWh", 0, 0},
	0x6000009E: {"Net Reactive Energy", "kVArh", 0, 0},
	0x6000009F: {"Apparent Energy", "kVAh", 0, 0},
	0x600000A1: {"0-1 Vrms CT", "Arms", 0, 0},
	0x600000A3: {"Disabled", "", 0, 0},
	0x600000A4: {"FastADC, 0-400k Ohms (autorange)", "Ohms", 0, 400000},
	0x600000A5: {"FastADC, 0-400k Ohms", "Ohms", 0, 400000},
	0x600000A6: {"FastADC, 0-200k Ohms", "Ohms", 0, 200000},
	0x600000A7: {"FastADC, 0-100k Ohms", "Ohms", 0, 100000},
	0x600000A8: {"FastADC, 0-50k Ohms", "Ohms", 0, 50000},
	0x600000A9: {"FastADC, 0-40k Ohms", "Ohms", 0, 40000},
	0x600000AA: {"FastADC, 0-20k Ohms", "Ohms", 0, 20000},
	0x600000AB: {"FastADC, 0-10k Ohms", "Ohms", 0, 10000},
	0x600000AC: {"FastADC, 0-5k Ohms", "Ohms", 0, 5000},
	0x600000AD: {"FastADC, 0-4k Ohms", "Ohms", 0, 4000},
	0x600000AE: {"FastADC, 0-2k Ohms", "Ohms", 0, 2000},
	0x600000AF: {"FastADC, 0-1k Ohms", "Ohms", 0, 1000},
	0x600000B0: {"FastADC, 0-500 Ohms", "Ohms", 0, 500},
	0x600000B1: {"FastADC, Thermistor: 2252 Curve", "°C", -40, 150},
	0x600000B2: {"FastADC, Thermistor: 3K Curve", "°C", -40, 150},
	0x600000B3: {"FastADC, Thermistor: 10K Type 2 Curve", "°C", -40, 150},
	0x600000B4: {"FastADC, Thermistor: 10K Type 3 Curve", "°C", -40, 150},
	0x600000B5: {"FastADC, Thermistor: Custom Curve", "°C", -40, 150},
	0x90000000: {"Digital Output", "", 0, 0},
	0x9000007B: {"5-30 VDC", "VDC", 5, 30},
	0x94000000: {"Simple Digital Output", "", 0, 0},
	0x94000078: {"Form C Relay", "", 0, 0},
	0xA000001B: {"0-20 mA", "mA", 0, 20},
	0xA000001C: {"0-24 mA", "mA", 0, 24},
	0xA000001D: {"4-20 mA", "mA", 4, 20},
	0xA000001F: {"±10 V", "V", -10, 10},
	0xA0000020: {"0-10 V", "V", 0, 10},
	0xA0000021: {"±5 V", "V", -5, 5},
	0xA0000022: {"0-5 V", "V", 0, 5},
	0xA0000023: {"Analog Hi-Z", "", 0, 0},
}
