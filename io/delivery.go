package io

// DeliveryReporter is an optional refinement for a driver whose ReadInputs
// serves an image that a loop of its own keeps (a poll loop, a stream, a
// shared-memory frame): such a ReadInputs succeeds from the moment the
// driver exists, before the field has answered once, so a nil error says
// nothing about whether the inputs are in it. Delivered says that: true
// once the driver has completed its first real read of the field — every
// source it polls has answered at least once — and true from then on,
// whatever happens to the connection afterwards.
//
// The runtime's start-up hold (runtime.Options.StartupWait) uses it: the
// first scan waits until the driver has delivered. A driver without it has
// delivered on its first ReadInputs that returns nil, which is exactly
// right for a driver that reads the field inside ReadInputs.
//
// Tags a driver synthesises about itself (a __Online companion, a status
// counter) are in its image from the start and are not a delivery.
type DeliveryReporter interface {
	Delivered() bool
}

// Delivered is d's DeliveryReporter answer, or true for a driver that does
// not report one (its read's error is then the whole story).
func Delivered(d Driver) bool {
	if dr, ok := d.(DeliveryReporter); ok {
		return dr.Delivered()
	}
	return true
}
