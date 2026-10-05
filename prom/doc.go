// Package prom is the Prometheus exposition driver: it scrapes a
// node_exporter-shaped `/metrics` endpoint and turns the samples into
// hw.Types struct tags (docs/design/it-drivers.md §3.3, §6.3). It is the
// simplest of the three IT-hardware drivers — one GET per source per poll,
// no session, no auth handshake — because the protocol itself is: text over
// HTTP, no framing, no state on the wire.
//
// text.go is the exposition-format parser (samples in, independent of
// anything hw- or manifest-shaped); manifest.go is the committed
// prometheus_manifest.yaml (decoded by ParseManifest, validated offline by
// Validate); driver.go is the io.Driver surface — New/Start/Stop wrapping
// hw.Base, and the Poll function that resolves one (source, class) into
// hw.Updates; rate.go is why prom keeps its own float64 counter instead of
// hw.Counter for the actual rate arithmetic (see the doc comment there);
// codegen/ turns a live or recorded scrape into the three generated files
// via the "node" profile; serve/ is the in-repo stand-in server.
package prom
