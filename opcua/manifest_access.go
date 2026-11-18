package opcua

// Manifest is the manifest the driver was built from: its bindings, for a
// caller that answers them in process (virtdev's in-process face).
func (d *Driver) Manifest() Manifest { return d.manifest }
