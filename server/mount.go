package server

import "net/http"

// A Mount adds one group of controller-only routes to the server. cmd/naut
// lists the groups it wants in Options.Mounts; the browser playground
// (cmd/naut-wasm) lists none, so the linker drops each group and everything
// reachable only from it. Handlers stay methods in package server and use
// s.audited, s.authorize, s.actorOf and writeJSON as the core routes do.
// Handler calls each Mount after the core routes, on each new mux.
type Mount func(s *Server, mux *http.ServeMux)

// FrameHook adds a block to a frame (a controller-only feature's state).
type FrameHook func(f *Frame)

// CheckoutForceView is what every frame says while a checkout force stands.
type CheckoutForceView struct {
	Run   string `json:"run"`
	Point string `json:"point"`
	Tag   string `json:"tag"`
	By    string `json:"by"`
	// RemainingMs is the time left, by the controller's monotonic clock.
	RemainingMs int64 `json:"remainingMs"`
}
