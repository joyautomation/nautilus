package retain

import "time"

// IOState is a live I/O configuration kept across a restart and a takeover
// (#706): what an apply over /api/io changed and the deployed manifest does
// not say yet. It is plain data; the project package (IOConfig) writes and
// reads it, and the runtime only carries it. A store written before it
// existed has none, and a binary older than it ignores it.
//
// Retain is a bridge to git, not a second source of truth: each unit records
// the manifest it was applied over (Base), and a restart or takeover
// re-applies it only while the deployed manifest still says that; a deploy
// that moved it wins, and the unit goes to Discarded.
type IOState struct {
	// Units are the applied units that differ from the deployed manifest,
	// keyed "driver" or "driver/device".
	Units map[string]IOUnit `json:"units,omitempty"`
	// Files are the project files the applied configuration was read from
	// that differ from the deployed ones, by project-relative path: Units are
	// rebuilt from the deployed files with these laid over them, and
	// `naut pull --io` writes them back into the project.
	Files map[string]string `json:"files,omitempty"`
	// Discarded are, per unit, the last kept change a deploy replaced (or
	// that could not be rebuilt): never applied, only kept so it can be
	// recovered by hand. DiscardedFiles are their files.
	Discarded      map[string]IOUnit `json:"discarded,omitempty"`
	DiscardedFiles map[string]string `json:"discardedFiles,omitempty"`
	// Provisional is an apply waiting for its devices to answer before it
	// reverts (revertIfSilent): it is not in Units until its watch settles,
	// so a restart or takeover meanwhile comes back on the configuration
	// before it. Recorded only so that comeback can say what it did not carry.
	Provisional *IOProvisional `json:"provisional,omitempty"`
}

// IOUnit is one kept unit: a whole driver, or one Modbus device.
type IOUnit struct {
	// Base is the deployed manifest's hash of the unit when the change was
	// applied over it; Hash is the applied unit's.
	Base string `json:"base"`
	Hash string `json:"hash"`
	// From is the commit the change was staged from, or "upload".
	From string    `json:"from,omitempty"`
	At   time.Time `json:"at,omitzero"`
	By   string    `json:"by,omitempty"`
	// Why says why a Discarded unit was dropped; empty in Units.
	Why string `json:"why,omitempty"`
}

// IOProvisional is an apply whose revert-if-silent watch had not settled.
type IOProvisional struct {
	Gen      int       `json:"gen"`
	From     string    `json:"from,omitempty"`
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at,omitzero"`
	Deadline time.Time `json:"deadline,omitzero"`
}
