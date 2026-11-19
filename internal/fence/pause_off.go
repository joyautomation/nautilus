//go:build !quorumtest

package fence

// pause is a no-op outside a quorumtest build (pause_on.go).
func pause(Point) {}

// BeforeSend is the last point before a send's system call; a no-op
// outside a quorumtest build.
func BeforeSend() {}
