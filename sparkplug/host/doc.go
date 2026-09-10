// Package host is the Sparkplug B *host application* driver for nautilus: one
// central project consumes many edge-node site projects off a single MQTT
// broker, presents their metrics as nautilus tags, sends operator writes back
// as NCMD/DCMD, publishes its own STATE birth/death certificate, and reports
// per-site comms health on /api/drivers.
//
// It is the mirror image of the parent sparkplug package, which is the *edge*
// node (and whose primaryhost.go watches a host's STATE, the other side of the
// same handshake). Everything this package needs from the parent is already
// exported: DecodePayload, Payload, Payload.Encode, Payload.OmitSeq, Metric,
// Template, spb.DataType, ValueFromMetric and StructDefsFromTemplates.
//
// Shape: a manifest-tier io.Driver (driver: {type: sparkplug-host}) plus
// `nautilus sparkplug import`, mirroring the eip driver end to end. The
// manifest is the only path — the runtime's tag set is fixed at compose time,
// so nothing is auto-created; metrics seen on the wire but not in the manifest
// go through the on-unknown policy and are surfaced by Discovered().
//
// New never dials. buildDriver runs inside `nautilus check` and
// `nautilus build`, i.e. in CI with no broker, so the driver is constructed
// offline and connects in Start — the same split eip makes.
//
// Files and ownership (see docs/design/sparkplug-host.md §7):
//
//	types.go      shared types — coordinate before editing
//	manifest.go   manifest structs' behaviour, structDefs()          (B1)
//	mapping.go    the tag-name sanitizer, InputNames/OutputNames     (B1)
//	state.go      the state machine: aliases, births, seq/reorder,
//	              deaths, templates, snapshot. No MQTT types          (B2)
//	mqtt.go       Config, New, Start/Stop, the reconnect loop,
//	              subscriptions, STATE/LWT, the NCMD/DCMD writer      (B3)
//	status.go     Status()                                           (B3)
package host
