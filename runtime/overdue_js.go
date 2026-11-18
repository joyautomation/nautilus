//go:build js

package runtime

// checkOverdue is off in a browser tab (the playground): a background tab's
// timers are throttled to a tick a second or far less, so a loop there
// misses its slots by design, and Liveness would read every one as starved
// (#858). A scan in progress still counts as hung: wasm has no preemption,
// so a scan runs to its end once started.
const checkOverdue = false
