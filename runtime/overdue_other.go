//go:build !js

package runtime

// checkOverdue: Liveness also flags a loop starved between scans (#858).
// Off only in a browser tab (overdue_js.go).
const checkOverdue = true
