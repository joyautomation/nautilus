package runtime

import nio "github.com/joyautomation/nautilus/io"

// StatsReader is a driver that reads the runtime's own health rather than
// a device: internal/project's `type: runtime` status driver (#850), whose
// inputs are a task's demotion, fault episode and lateness counters, so
// alarms and the historian can see them. The runtime hands it Stats when it
// goes live (Run), and again for a driver swapped in later. Stats takes the
// runtime's locks: call it from the driver's own goroutine, never from
// ReadInputs (the main scan holds them around the read).
type StatsReader interface {
	ReadStatsFrom(stats func() ScanStats)
}

// bindStatsReaders hands Stats to every StatsReader in d, an io.Multi's
// children included. Run calls it before the first scan, and ReplaceDriver
// for a driver swapped into a running set.
func (r *Runtime) bindStatsReaders(d nio.Driver) {
	switch x := d.(type) {
	case nil:
	case *nio.Multi:
		for _, c := range x.Children() {
			r.bindStatsReaders(c.Driver)
		}
	case StatsReader:
		x.ReadStatsFrom(r.Stats)
	}
}
