package fence

// Point is one of the three places between the gate check and the send
// where a quorumtest build can stall a send (test plan §3.4).
type Point int

const (
	// AfterCheck: after the gate is read, before the deadline is computed.
	AfterCheck Point = iota
	// AfterDeadline: after the deadline is set, before the send.
	AfterDeadline
	// RightBeforeSend: right before the send system call (BeforeSend).
	RightBeforeSend
)
