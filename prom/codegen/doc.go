// Package codegen turns a live or recorded Prometheus scrape into the files
// a manifest project consumes: prometheus_manifest.yaml (decoded by
// prom.LoadManifest), tags/prometheus.yaml (internal/tagfile), and
// hw_types.st (hw.TypesST). `naut prometheus import` is the command
// wrapper; everything here is a PURE function of the recorded input, so a
// regeneration is byte-identical and the same bytes come off a live device
// and off its recording (docs/design/it-drivers.md §8, §9.3).
//
// All exporter/metric-name knowledge lives here, in the "node" profile
// (node.go) — the driver itself (prom/driver.go) is a dumb executor of
// whatever bindings the manifest names, exactly like the other two drivers'
// split (docs/design/it-drivers.md §3, first paragraph).
package codegen
