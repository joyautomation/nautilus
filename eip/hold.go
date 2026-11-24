package eip

import nio "github.com/joyautomation/nautilus/io"

// ForgetOutputs is the driver's io.OutputsForgetter (#710 (b2), the
// takeover hold): it forgets what it wrote and what it has queued, so no
// command from an earlier leadership goes out while the hold holds.
func (d *Driver) ForgetOutputs() { d.forgetWritten() }

var _ nio.OutputsForgetter = (*Driver)(nil)
