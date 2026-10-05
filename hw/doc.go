// Package hw is the shared substrate of the IT-hardware drivers — snmp,
// redfish and prom — the way runtime/ is the substrate of the languages:
// the UDT set every one of them delivers (a SwitchPort is the same struct
// whether it came off IF-MIB or a vendor REST API), the per-source poll loop
// with its backoff and quality rules, counter→rate, and the binding
// vocabulary the three manifests share. docs/design/it-drivers.md is the
// brief; §2 there is the contract this package's Types table implements.
//
// A protocol package supplies two things: a Poll function that fetches one
// (source, scan class) and returns member updates, and a Write function for
// the rare opt-in command. Everything else — snapshots, __Online companions,
// Quality, Health, scan-class routing, io.Driver plumbing — is Base.
//
// hw is pure stdlib plus lang/ir and lang/stgen (the IEC type renderer), so
// the protocol packages carry their own dependencies (gosnmp) and this one
// never does.
package hw
