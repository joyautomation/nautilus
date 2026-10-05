package profiles

import (
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// vendorHook extends the switch profile for one enterprise's private MIB,
// keyed by sysObjectID prefix: the place CPU, memory, temperature, PSU and
// fan objects go once they are CONFIRMED on the hardware (brief §12.1).
type vendorHook struct {
	prefix string
	name   string
	extend func(w walk.Walk, root map[string]snmp.Member, b *builder)
}

var vendorHooks = []vendorHook{
	{prefix: "1.3.6.1.4.1.52642", name: "FS.COM FSOS", extend: fsExtension},
}

// fsExtension — FS.COM switches (enterprise 52642, FSOS). ── VENDOR HOOK ──
//
// What is known, from one read-only walk of 1.3.6.1.4.1.52642 on an
// S3900-24T4S-R running FSOS 2.2.0F (office bench, 2026-09):
//
//	.52642.…3507.1.1  version string / serial / hardware revision
//	.52642.…3507.1.2  four integers — 268435456, 154724280, 113711176, 43 —
//	                  that LOOK like memory total/used/free/percent. UNCONFIRMED:
//	                  deliberately not bound (a wrong MemPct on an operator
//	                  screen is worse than a 0 that says "unknown").
//	.52642.…9.225.1   model / serial / MAC / version
//
// No CPU, temperature, PSU or fan objects were identified. When the bench
// confirms one (e.g. MemPct moving with load), bind it here — root["MemPct"]
// = bind(oid, hw.Binding{}) — and add the object to the switch fixture so
// the golden test pins it. Until then the hook binds nothing and says so.
func fsExtension(w walk.Walk, root map[string]snmp.Member, b *builder) {
	b.notes = append(b.notes, "FS.COM (enterprise 52642) vendor hook: no FSOS private objects are bound yet (memory candidates under .3507.1.2 are unconfirmed) — see snmp/profiles/vendor.go")
}
