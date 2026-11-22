package server

import "net/http"

// A Mount adds one group of controller-only routes to the server. cmd/naut
// lists the groups it wants in Options.Mounts; the browser playground
// (cmd/naut-wasm) lists none, so the linker drops each group and everything
// reachable only from it. Handlers stay methods in package server and use
// s.audited, s.authorize, s.actorOf and writeJSON as the core routes do.
//
// A Mount's routes must go through s.audited and s.authorize like the core
// routes: mounting is about what is linked, never about who may call it.
//
// Handler calls each Mount after the core routes, on each new mux it builds, so
// a Mount may run more than once: state a group keeps belongs in what the
// Mount closes over, not in the Mount's body.
type Mount func(s *Server, mux *http.ServeMux)

// FrameHook adds a block to a frame (a controller-only feature's state).
type FrameHook func(s *Server, f *Frame)

// CheckoutForceView is what every frame says while a checkout force stands.
type CheckoutForceView struct {
	Run   string `json:"run"`
	Point string `json:"point"`
	Tag   string `json:"tag"`
	By    string `json:"by"`
	// RemainingMs is the time left, by the controller's monotonic clock.
	RemainingMs int64 `json:"remainingMs"`
}
