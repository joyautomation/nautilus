package server

import "net/http"

// A Mount adds one group of controller-only routes to the server. New
// installs the controller's groups (mounts_controller.go) and any a caller
// lists in Options.Mounts; the browser playground (NewPlayground,
// cmd/naut-wasm) installs none, so the linker drops each group and everything
// reachable only from it. Handlers stay methods in package server and use
// s.audited, s.authorize, s.actorOf and writeJSON as the core routes do.
//
// Handler calls each Mount after the core routes, on each new mux it builds, so
// a Mount may run more than once: state a group keeps belongs in what the
// Mount closes over, not in the Mount's body.
type Mount func(s *Server, mux *http.ServeMux)
