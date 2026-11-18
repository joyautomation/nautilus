package device

// StartupTimeout exposes startupTimeout to the external tests.
var StartupTimeout = startupTimeout

// SetRPCTestHook installs (nil removes) a func run inside the device lock
// before each PNIO request.
func SetRPCTestHook(f func(op uint16)) {
	if f == nil {
		rpcTestHook.Store(nil)
		return
	}
	rpcTestHook.Store(&f)
}
