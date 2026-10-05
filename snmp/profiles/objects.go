package profiles

import (
	"strings"

	"github.com/joyautomation/nautilus/snmp/walk"
)

// objects names the MIB objects the profiles know, for `naut snmp browse`:
// a commissioning tech reads "ifHCInOctets.3 = 812345678", not a 20-arc
// number. It is a naming aid only — nothing binds by name.
var objects = map[string]string{
	"1.3.6.1.2.1.1.1": "sysDescr", "1.3.6.1.2.1.1.2": "sysObjectID", "1.3.6.1.2.1.1.3": "sysUpTime",
	"1.3.6.1.2.1.1.4": "sysContact", "1.3.6.1.2.1.1.5": "sysName", "1.3.6.1.2.1.1.6": "sysLocation",
	"1.3.6.1.2.1.1.7": "sysServices", "1.3.6.1.2.1.2.1": "ifNumber",

	ifIndexCol: "ifIndex", ifDescr: "ifDescr", ifType: "ifType", ifTable + ".4": "ifMtu", ifSpeed: "ifSpeed",
	ifTable + ".6": "ifPhysAddress", ifAdminStatus: "ifAdminStatus", ifOperStatus: "ifOperStatus",
	ifTable + ".9": "ifLastChange", ifInOctets: "ifInOctets", ifTable + ".11": "ifInUcastPkts",
	ifInDiscards: "ifInDiscards", ifInErrors: "ifInErrors", ifOutOctets: "ifOutOctets",
	ifTable + ".17": "ifOutUcastPkts", ifOutDiscards: "ifOutDiscards", ifOutErrors: "ifOutErrors",
	ifName: "ifName", ifHCInOctets: "ifHCInOctets", ifHCOutOctets: "ifHCOutOctets",
	ifHighSpeed: "ifHighSpeed", ifAlias: "ifAlias",

	entPhysicalTable + ".2": "entPhysicalDescr", entPhysicalClass: "entPhysicalClass",
	entPhysicalName: "entPhysicalName", entPhysicalSerialNum: "entPhysicalSerialNum",
	entPhysicalModelName: "entPhysicalModelName",

	entPhySensorType: "entPhySensorType", entPhySensorScale: "entPhySensorScale",
	entPhySensorPrecision: "entPhySensorPrecision", entPhySensorValue: "entPhySensorValue",
	entPhySensorOperStatus: "entPhySensorOperStatus",

	"1.3.6.1.2.1.105.1.1.1.3": "pethPsePortAdminEnable", pethPsePortDetectionStatus: "pethPsePortDetectionStatus",

	"1.3.6.1.2.1.33.1.1.1": "upsIdentManufacturer", "1.3.6.1.2.1.33.1.1.2": "upsIdentModel",
	"1.3.6.1.2.1.33.1.1.5": "upsIdentName", "1.3.6.1.2.1.33.1.2.1": "upsBatteryStatus",
	"1.3.6.1.2.1.33.1.2.3": "upsEstimatedMinutesRemaining", "1.3.6.1.2.1.33.1.2.4": "upsEstimatedChargeRemaining",
	"1.3.6.1.2.1.33.1.2.7": "upsBatteryTemperature", "1.3.6.1.2.1.33.1.3.3.1.3": "upsInputVoltage",
	"1.3.6.1.2.1.33.1.4.1": "upsOutputSource", "1.3.6.1.2.1.33.1.4.4.1.2": "upsOutputVoltage",
	"1.3.6.1.2.1.33.1.4.4.1.5": "upsOutputPercentLoad", "1.3.6.1.2.1.33.1.6.1": "upsAlarmsPresent",

	cpsUPS + ".1.1.1": "upsBaseIdentModel", cpsUPS + ".1.1.2": "upsBaseIdentName",
	cpsUPS + ".1.2.3": "upsAdvanceIdentSerialNumber", cpsUPS + ".2.1.1": "upsBaseBatteryStatus",
	cpsUPS + ".2.2.1": "upsAdvanceBatteryCapacity", cpsUPS + ".2.2.3": "upsAdvanceBatteryTemperature",
	cpsUPS + ".2.2.4": "upsAdvanceBatteryRunTimeRemaining", cpsUPS + ".2.2.5": "upsAdvanceBatteryReplaceIndicator",
	cpsUPS + ".3.2.1": "upsAdvanceInputLineVoltage", cpsUPS + ".4.1.1": "upsBaseOutputStatus",
	cpsUPS + ".4.2.1": "upsAdvanceOutputVoltage", cpsUPS + ".4.2.3": "upsAdvanceOutputLoad",

	cpsPDU + ".1.1": "ePDUIdentName", cpsPDU + ".1.5": "ePDUIdentModelNumber", cpsPDU + ".1.6": "ePDUIdentSerialNumber",
	cpsPDU + ".1.8": "ePDUIdentDeviceNumOutlets", cpsPDU + ".2.3.1.1.2": "ePDULoadStatusLoad",
	cpsPDU + ".2.3.1.1.3": "ePDULoadStatusLoadState", cpsPDU + ".2.3.1.1.7": "ePDULoadStatusActivePower",
	cpsPDU + ".3.3.1.1.1": "ePDUOutletControlIndex", ePDUOutletControlOutletName: "ePDUOutletControlOutletName",
	ePDUOutletControlOutletCommand: "ePDUOutletControlOutletCommand",
}

// ObjectName renders an OID with the longest known object name and its
// instance suffix ("ifHCInOctets.3"), or the numeric OID when no profile
// knows it.
func ObjectName(oid string) string {
	for p := oid; p != ""; p = walk.Parent(p) {
		if name, ok := objects[p]; ok {
			return name + strings.TrimPrefix(oid, p)
		}
	}
	return oid
}
