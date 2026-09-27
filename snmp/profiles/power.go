package profiles

// The power profiles. ALL THREE ARE UNVERIFIED AGAINST HARDWARE: they are
// written from the published MIB files — RFC 1628 (UPS-MIB) and CyberPower's
// CPS-MIB (as distributed by LibreNMS, mibs/cyberpower/CPS-MIB) — and each
// OID below carries its MIB object name so the binding can be checked
// against the module. The office PDU41001 and OR1500PFCRT2U are not on the
// bench yet (brief §12.2); when they are, `naut snmp browse --record` them,
// import the recording, and flip Verified once the members read true.

import (
	"fmt"
	"strconv"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// UPS-MIB (RFC 1628), upsMIB = 1.3.6.1.2.1.33, upsObjects = .1.
const (
	upsIdentModel                = "1.3.6.1.2.1.33.1.1.2.0"     // upsIdentModel
	upsIdentName                 = "1.3.6.1.2.1.33.1.1.5.0"     // upsIdentName
	upsBatteryStatus             = "1.3.6.1.2.1.33.1.2.1.0"     // unknown(1) batteryNormal(2) batteryLow(3) batteryDepleted(4)
	upsEstimatedMinutesRemaining = "1.3.6.1.2.1.33.1.2.3.0"     // minutes
	upsEstimatedChargeRemaining  = "1.3.6.1.2.1.33.1.2.4.0"     // percent
	upsBatteryTemperature        = "1.3.6.1.2.1.33.1.2.7.0"     // degrees Centigrade
	upsInputVoltage1             = "1.3.6.1.2.1.33.1.3.3.1.3.1" // upsInputVoltage, line 1, RMS volts
	upsOutputSource              = "1.3.6.1.2.1.33.1.4.1.0"     // … normal(3) bypass(4) battery(5) …
	upsOutputVoltage1            = "1.3.6.1.2.1.33.1.4.4.1.2.1" // upsOutputVoltage, line 1, RMS volts
	upsOutputPercentLoad1        = "1.3.6.1.2.1.33.1.4.4.1.5.1" // upsOutputPercentLoad, line 1
	upsAlarmsPresent             = "1.3.6.1.2.1.33.1.6.1.0"     // Gauge32: active alarm rows
	upsIdentRoot                 = "1.3.6.1.2.1.33.1.1"
)

// CPS-MIB, cps = 1.3.6.1.4.1.3808, hardware = cps.1.1, ups = hardware.1,
// ePDU = hardware.3.
const (
	cpsUPS                            = "1.3.6.1.4.1.3808.1.1.1"
	upsBaseIdentModel                 = cpsUPS + ".1.1.1.0" // DisplayString
	upsBaseIdentName                  = cpsUPS + ".1.1.2.0" // DisplayString
	upsAdvanceIdentSerialNumber       = cpsUPS + ".1.2.3.0" // DisplayString
	upsBaseBatteryStatus              = cpsUPS + ".2.1.1.0" // unknown(1) batteryNormal(2) batteryLow(3) batteryNotPresent(4)
	upsAdvanceBatteryCapacity         = cpsUPS + ".2.2.1.0" // Gauge, percent
	upsAdvanceBatteryTemperature      = cpsUPS + ".2.2.3.0" // Gauge, Celsius
	upsAdvanceBatteryRunTimeRemaining = cpsUPS + ".2.2.4.0" // TimeTicks
	upsAdvanceBatteryReplaceIndicator = cpsUPS + ".2.2.5.0" // noBatteryNeedsReplacing(1) batteryNeedsReplacing(2)
	upsAdvanceInputLineVoltage        = cpsUPS + ".3.2.1.0" // Gauge, 1/10 VAC
	upsBaseOutputStatus               = cpsUPS + ".4.1.1.0" // unknown(1) onLine(2) onBattery(3) …
	upsAdvanceOutputVoltage           = cpsUPS + ".4.2.1.0" // Gauge, 1/10 VAC
	upsAdvanceOutputLoad              = cpsUPS + ".4.2.3.0" // Gauge, percent

	cpsPDU                         = "1.3.6.1.4.1.3808.1.1.3"
	ePDUIdentName                  = cpsPDU + ".1.1.0"       // DisplayString
	ePDUIdentModelNumber           = cpsPDU + ".1.5.0"       // DisplayString
	ePDUIdentSerialNumber          = cpsPDU + ".1.6.0"       // DisplayString
	ePDULoadStatusLoad1            = cpsPDU + ".2.3.1.1.2.1" // Gauge, tenths of amps, phase/bank row 1
	ePDULoadStatusLoadState1       = cpsPDU + ".2.3.1.1.3.1" // loadNormal(1) loadLow(2) loadNearOverload(3) loadOverload(4)
	ePDULoadStatusActivePower1     = cpsPDU + ".2.3.1.1.7.1" // INTEGER watts (newer firmware)
	ePDUOutletControlOutletName    = cpsPDU + ".3.3.1.1.2"   // column, indexed by outlet
	ePDUOutletControlOutletCommand = cpsPDU + ".3.3.1.1.4"   // column: reads immediateOn(1) when on, immediateOff(2) when off
)

func upsRFC1628() Profile {
	return Profile{
		Name: "ups-rfc1628",
		Desc: "UPS over the standard UPS-MIB (RFC 1628) — unverified against hardware",
		Probe: func(w walk.Walk) bool {
			return len(w.Subtree(upsIdentRoot)) > 0
		},
		Build: func(w walk.Walk, o Options) (Result, error) {
			b := &builder{w: w}
			m := map[string]snmp.Member{}
			b.opt(m, "Name", upsIdentName, hw.Binding{}, "upsIdentName.0")
			b.opt(m, "Model", upsIdentModel, hw.Binding{}, "upsIdentModel.0")
			b.opt(m, "OnBattery", upsOutputSource, hw.Binding{Eq: 5}, "upsOutputSource.0")
			// batteryLow(3) and batteryDepleted(4) are both "low".
			b.opt(m, "LowBattery", upsBatteryStatus, hw.Binding{Map: map[string]any{"1": false, "2": false, "3": true, "4": true}}, "upsBatteryStatus.0")
			// Any row in upsAlarmTable; a Gauge32 > 0 coerces to true.
			b.opt(m, "Fault", upsAlarmsPresent, hw.Binding{}, "upsAlarmsPresent.0")
			b.opt(m, "ChargePct", upsEstimatedChargeRemaining, hw.Binding{}, "upsEstimatedChargeRemaining.0")
			b.opt(m, "RuntimeMin", upsEstimatedMinutesRemaining, hw.Binding{}, "upsEstimatedMinutesRemaining.0")
			b.opt(m, "LoadPct", upsOutputPercentLoad1, hw.Binding{}, "upsOutputPercentLoad.1")
			b.opt(m, "InputV", upsInputVoltage1, hw.Binding{}, "upsInputVoltage.1")
			b.opt(m, "OutputV", upsOutputVoltage1, hw.Binding{}, "upsOutputVoltage.1")
			b.opt(m, "BatteryTempC", upsBatteryTemperature, hw.Binding{}, "upsBatteryTemperature.0")
			b.notes = append(b.notes,
				"UPS.Serial: RFC 1628 has no serial number object — left empty",
				"UPS.BatteryFault: RFC 1628 reports it only as a row in upsAlarmTable (upsAlarmBatteryBad), which a fixed OID cannot bind — left false; Fault covers any alarm")
			return Result{Profile: "ups-rfc1628", Instances: []Instance{{Type: "UPS", Members: m}}, Notes: b.notes}, nil
		},
	}
}

func upsCyberPower() Profile {
	return Profile{
		Name: "ups-cyberpower",
		Desc: "CyberPower UPS over CPS-MIB (enterprise 3808) — unverified against hardware",
		Probe: func(w walk.Walk) bool {
			return len(w.Subtree(cpsUPS+".1")) > 0
		},
		Build: func(w walk.Walk, o Options) (Result, error) {
			b := &builder{w: w}
			m := map[string]snmp.Member{}
			b.opt(m, "Name", upsBaseIdentName, hw.Binding{}, "upsBaseIdentName.0")
			b.opt(m, "Model", upsBaseIdentModel, hw.Binding{}, "upsBaseIdentModel.0")
			b.opt(m, "Serial", upsAdvanceIdentSerialNumber, hw.Binding{}, "upsAdvanceIdentSerialNumber.0")
			b.opt(m, "OnBattery", upsBaseOutputStatus, hw.Binding{Eq: 3}, "upsBaseOutputStatus.0")
			b.opt(m, "LowBattery", upsBaseBatteryStatus, hw.Binding{Eq: 3}, "upsBaseBatteryStatus.0")
			b.opt(m, "BatteryFault", upsAdvanceBatteryReplaceIndicator, hw.Binding{Eq: 2}, "upsAdvanceBatteryReplaceIndicator.0")
			b.opt(m, "ChargePct", upsAdvanceBatteryCapacity, hw.Binding{}, "upsAdvanceBatteryCapacity.0")
			// TimeTicks are 1/100 s: minutes = ticks / 6000.
			b.opt(m, "RuntimeMin", upsAdvanceBatteryRunTimeRemaining, hw.Binding{Scale: 1.0 / 6000}, "upsAdvanceBatteryRunTimeRemaining.0")
			b.opt(m, "LoadPct", upsAdvanceOutputLoad, hw.Binding{}, "upsAdvanceOutputLoad.0")
			b.opt(m, "InputV", upsAdvanceInputLineVoltage, hw.Binding{Scale: 0.1}, "upsAdvanceInputLineVoltage.0")
			b.opt(m, "OutputV", upsAdvanceOutputVoltage, hw.Binding{Scale: 0.1}, "upsAdvanceOutputVoltage.0")
			b.opt(m, "BatteryTempC", upsAdvanceBatteryTemperature, hw.Binding{}, "upsAdvanceBatteryTemperature.0")
			m["Fault"] = derived("OnBattery || LowBattery || BatteryFault")
			b.notes = append(b.notes,
				"UPS.Fault is derived (OnBattery || LowBattery || BatteryFault): CPS-MIB has no single alarms-present object")
			return Result{Profile: "ups-cyberpower", Instances: []Instance{{Type: "UPS", Members: m}}, Notes: b.notes}, nil
		},
	}
}

func pduCyberPower() Profile {
	return Profile{
		Name: "pdu-cyberpower",
		Desc: "CyberPower switched PDU over CPS-MIB ePDU (enterprise 3808) — unverified against hardware",
		Probe: func(w walk.Walk) bool {
			return len(w.Subtree(cpsPDU+".1")) > 0
		},
		Build: func(w walk.Walk, o Options) (Result, error) {
			b := &builder{w: w}
			root := map[string]snmp.Member{}
			b.opt(root, "Name", ePDUIdentName, hw.Binding{}, "ePDUIdentName.0")
			b.opt(root, "Model", ePDUIdentModelNumber, hw.Binding{}, "ePDUIdentModelNumber.0")
			b.opt(root, "Serial", ePDUIdentSerialNumber, hw.Binding{}, "ePDUIdentSerialNumber.0")
			b.opt(root, "Amps", ePDULoadStatusLoad1, hw.Binding{Scale: 0.1}, "ePDULoadStatusLoad.1")
			b.opt(root, "Watts", ePDULoadStatusActivePower1, hw.Binding{}, "ePDULoadStatusActivePower.1")
			b.opt(root, "Overload", ePDULoadStatusLoadState1, hw.Binding{Map: map[string]any{"1": false, "2": false, "3": true, "4": true}}, "ePDULoadStatusLoadState.1")

			var outlets []int
			for _, s := range rows(w, ePDUOutletControlOutletCommand) {
				if n, err := strconv.Atoi(s); err == nil {
					outlets = append(outlets, n)
				}
			}
			sortInts(outlets)
			root["OutletCount"] = cnst(len(outlets))
			res := Result{Profile: "pdu-cyberpower", Instances: []Instance{{Type: "PDU", Members: root}}}
			width := 2
			if len(outlets) > 0 {
				width = max(2, padWidth(outlets[len(outlets)-1]))
			}
			for _, n := range outlets {
				i := strconv.Itoa(n)
				m := map[string]snmp.Member{
					"Index": cnst(n),
					"On":    bind(ePDUOutletControlOutletCommand+"."+i, hw.Binding{Eq: 1}),
				}
				if has(w, ePDUOutletControlOutletName+"."+i) {
					m["Name"] = bind(ePDUOutletControlOutletName+"."+i, hw.Binding{})
				}
				res.Instances = append(res.Instances, Instance{Suffix: "_Outlet" + padded(n, width), Type: "PDUOutlet", Members: m})
			}
			b.notes = append(b.notes,
				"PDU.Amps/Watts/Overload read phase/bank row 1 — the whole load on a single-phase, single-bank PDU",
				"PDUOutlet.Amps/Watts: per-outlet metering objects are not bound — left at 0",
				fmt.Sprintf("commands are opt-in: to switch an outlet, add by hand `writes: [{name: <PDU>_Outlet03_Cmd, tag: <PDU>_Outlet03, member: On, oid: %s.3, set: {true: 1, false: 2}}]`", ePDUOutletControlOutletCommand))
			res.Notes = b.notes
			return res, nil
		},
	}
}
