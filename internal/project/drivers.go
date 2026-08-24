package project

import (
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/sparkplug"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
)

// DriverStatus adapts the field driver's and Sparkplug node's health into the
// generic server.DriverStatus envelope the HMI components render. Wired as
// server.Options.Drivers so /api/drivers and the stream frame carry it, with
// the server package staying free of any driver dependency.
func (p *Project) DriverStatus(node *sparkplug.Node) func() []server.DriverStatus {
	eipDrv, hasEIP := p.Runtime.Driver.(*eip.Driver)
	hostDrv, hasHost := p.Runtime.Driver.(*sphost.Driver)
	if !hasEIP && !hasHost && node == nil {
		return nil
	}
	return func() []server.DriverStatus {
		var out []server.DriverStatus
		if hasEIP {
			out = append(out, eipStatus(eipDrv.Health()))
		}
		if hasHost {
			out = append(out, hostStatus(hostDrv.Status()))
		}
		if node != nil {
			out = append(out, sparkplugStatus(node.Status()))
		}
		return out
	}
}

func eipStatus(h eip.Health) server.DriverStatus {
	s := server.DriverStatus{
		Kind:      "ethernet-ip",
		Name:      h.Host,
		Detail:    fmt.Sprintf("%s · slot %d", h.Host, h.Slot),
		LastError: h.LastError,
		SinceMs:   h.SinceMs,
	}
	switch {
	case h.Connected:
		s.State = "connected"
		s.Message = fmt.Sprintf("Polling %d tags", h.Tags)
	case h.LastError != "":
		s.State = "error"
		s.Message = "Connect failed — retrying"
	default:
		s.State = "connecting"
		s.Message = "Connecting to controller"
	}
	// A connected driver whose polls are all failing is degraded, not healthy.
	if h.Connected && h.Polls == 0 && h.PollErrors > 0 {
		s.State = "degraded"
		s.Message = "Connected, but reads are failing"
	}
	s.Metrics = []server.DriverMetric{
		{Label: "tags", Value: float64(h.Tags)},
		{Label: "polls", Value: float64(h.Polls)},
		{Label: "poll errors", Value: float64(h.PollErrors)},
		{Label: "reconnects", Value: float64(h.Reconnects)},
	}
	if h.LeafMode > 0 {
		s.Metrics = append(s.Metrics, server.DriverMetric{Label: "leaf structs", Value: float64(h.LeafMode)})
	}
	if h.LastPollMs > 0 {
		s.Metrics = append(s.Metrics, server.DriverMetric{
			Label: "last poll",
			Text:  agoText(h.LastPollMs),
		})
	}
	return s
}

// hostStatus adapts the Sparkplug host application's health. Its per-node
// rows land on Devices — the same shape a Sparkplug edge node's devices take
// — so DriverStatusPanel/DriverStatusCard render 60 sites' comms status with
// no HMI change at all. Device sub-rows flatten as "<edge>/<device>".
//
// # Why nothing in here is allowed to free-run
//
// For a 55-site host this status is ~13 kB, and a delta stream sends it only
// when it CHANGES — the server hashes the block and gates on that hash. So
// "what counts as a change" is this function's problem, and anything here
// that moves on its own defeats the whole mechanism: it puts 13 kB on four
// frames a second and buys nothing, because a value that is only refreshed
// when something ELSE changes was never live to begin with.
//
// Measured on the Pomona host: this block rode every single frame (3.0 MB a
// minute to a client subscribed to no tags at all) because of exactly three
// free-runners — Extra carried each node's LastMsgMs and Seq, which move on
// every NDATA; the metrics grid carried a total message count and an age
// rendered from the clock; and each site's Devices row rendered "born 3m" /
// "stale 4s", which re-renders every second while a site is stale.
//
// The fix is to report the plant CATEGORICALLY. What an operator watches for
// — a site offline, a site gone stale, a rebirth, a sequence gap, a metric
// nobody manifested — all still bust the hash the moment it happens. What
// merely ticks is not reported here at all; moments (BirthMs) are, because
// a moment changes only when the event does, and a reader renders the age
// from it. See server.DriverStatus for the general contract, and its
// Volatile / VolatileExtra fields for how a status that genuinely needs a
// free-running readout declares one.
func hostStatus(st sphost.Status) server.DriverStatus {
	group := strings.Join(st.Groups, ", ")
	detail := st.Broker
	if group != "" {
		detail += " · " + group
	}
	s := server.DriverStatus{
		Kind:      "sparkplug-host",
		Name:      st.HostID,
		Detail:    detail,
		SinceMs:   st.StateOnlineMs,
		LastError: st.LastError,
	}

	online, born, stale := 0, 0, 0
	for _, n := range st.Nodes {
		if n.Online {
			online++
		}
		if n.BirthMs > 0 {
			born++
		}
		// A site that is online but has stopped talking is the fault the
		// message counter used to hint at, named instead of inferred.
		if n.Online && n.Stale {
			stale++
		}
	}
	// error (broker down) → connecting (connected, nothing has birthed) →
	// degraded (a site is dark, or strict discovery saw an unbound metric)
	// → connected.
	switch {
	case !st.Connected:
		s.State = "error"
		s.Message = "Broker unreachable"
		s.SinceMs = 0
	case born == 0:
		s.State = "connecting"
		s.Message = "Connected, waiting for births"
	case st.Degraded:
		s.State = "degraded"
		s.Message = fmt.Sprintf("%d unmanifested metric(s) — re-run the import", st.Unknown)
	case online < len(st.Nodes):
		s.State = "degraded"
		s.Message = fmt.Sprintf("%d of %d sites offline", len(st.Nodes)-online, len(st.Nodes))
	default:
		s.State = "connected"
		s.Message = fmt.Sprintf("Consuming %d sites", len(st.Nodes))
	}

	// Every readout here steps on an EVENT — a site dropping, a rebirth, a
	// sequence gap, a metric nobody manifested — so each one moving is worth
	// a frame. The two that used to be here and are not any more (the total
	// message count, and the age of the last message) stepped on every
	// message instead: "is traffic flowing" is answered better, and at rest,
	// by how many sites have gone stale.
	s.Metrics = []server.DriverMetric{
		{Label: "sites online", Value: float64(online), Text: fmt.Sprintf("%d / %d", online, len(st.Nodes))},
		{Label: "sites stale", Value: float64(stale)},
		{Label: "rebirths", Value: float64(st.Rebirths)},
		{Label: "seq gaps", Value: float64(st.SeqGaps)},
		{Label: "unknown metrics", Value: float64(st.Unknown)},
	}
	if st.WriteDrops > 0 {
		s.Metrics = append(s.Metrics, server.DriverMetric{Label: "write drops", Value: float64(st.WriteDrops)})
	}
	if st.QueuedWrites > 0 {
		// Commands waiting for a dark site to come back — a gauge, so it
		// clears itself the moment the site births and they go out.
		s.Metrics = append(s.Metrics, server.DriverMetric{
			Label: "queued writes", Value: float64(st.QueuedWrites)})
	}
	for _, n := range st.Nodes {
		s.Devices = append(s.Devices, server.DriverDevice{
			ID:     n.EdgeNode,
			Online: n.Online,
			Detail: nodeDetail(n),
		})
		for _, dv := range n.Devices {
			detail := "offline"
			if dv.Online {
				detail = fmt.Sprintf("%d tags", dv.Metrics)
			}
			s.Devices = append(s.Devices, server.DriverDevice{
				ID:     n.EdgeNode + "/" + dv.ID,
				Online: dv.Online,
				Detail: detail,
			})
		}
	}
	s.Extra = map[string]any{"nodes": nodeRows(st.Nodes)}
	return s
}

// nodeRows projects the host's node table into Extra["nodes"] — the
// structured twin of the flattened Devices list, for the richer per-site
// page docs/design/sparkplug-host.md §11 anticipates.
//
// It is deliberately NOT sphost.NodeStatus itself. That struct carries Seq
// and LastMsgMs, which step on every message the host receives, and marshalling
// it whole was what kept this block on every frame of the Pomona stream: 55
// nodes × two counters, nested one level down, where no top-level exclusion
// could reach them. Everything here either holds still or moves for a reason
// — the flags an operator acts on, the tag counts, and BirthMs, which is a
// moment rather than an age and so changes only when the site actually
// (re)births.
//
// If a page ever does need per-node freshness on the wire, the way to add it
// is server.DriverStatus.VolatileExtra with the nested path
// ("nodes.*.lastMsgMs"), not by putting a free-runner back in this shape.
func nodeRows(nodes []sphost.NodeStatus) []nodeRow {
	out := make([]nodeRow, 0, len(nodes))
	for _, n := range nodes {
		r := nodeRow{
			Group: n.Group, EdgeNode: n.EdgeNode,
			Online: n.Online, Stale: n.Stale, BdSeq: n.BdSeq,
			BirthMs: n.BirthMs, Metrics: n.Metrics, QueuedWrites: n.QueuedWrites,
		}
		for _, d := range n.Devices {
			r.Devices = append(r.Devices, deviceRow{
				ID: d.ID, Online: d.Online, BirthMs: d.BirthMs, Metrics: d.Metrics,
			})
		}
		out = append(out, r)
	}
	return out
}

// nodeRow is one site in Extra["nodes"]; deviceRow is one of its devices.
// Lower-camel JSON names, like every other field this API puts on the wire.
type nodeRow struct {
	Group        string      `json:"group,omitempty"`
	EdgeNode     string      `json:"edgeNode"`
	Online       bool        `json:"online"`
	Stale        bool        `json:"stale,omitempty"`
	BdSeq        int64       `json:"bdSeq"`
	BirthMs      int64       `json:"birthMs,omitempty"`
	Metrics      int         `json:"metrics"`
	QueuedWrites int         `json:"queuedWrites,omitempty"`
	Devices      []deviceRow `json:"devices,omitempty"`
}

type deviceRow struct {
	ID      string `json:"id"`
	Online  bool   `json:"online"`
	BirthMs int64  `json:"birthMs,omitempty"`
	Metrics int    `json:"metrics"`
}

// nodeDetail is one site's row text: how many tags it carries, and why it
// is not carrying them if it isn't. Stale is called out separately from
// offline — a node that stopped talking without an NDEATH is a different
// fault (network, broker) from one that said goodbye.
//
// No AGE in here, deliberately. This string is hashed to decide whether the
// whole 13 kB status goes on the wire, and "stale 4s" re-renders every
// second: one sulking site would have put the block on every frame. The
// moments themselves ride Extra["nodes"] (birthMs), where a reader can
// render the age against the status's own observation time.
func nodeDetail(n sphost.NodeStatus) string {
	switch {
	case !n.Online:
		return "offline"
	case n.Stale:
		return fmt.Sprintf("%d tags · stale", n.Metrics)
	default:
		return fmt.Sprintf("%d tags", n.Metrics)
	}
}

func sparkplugStatus(st sparkplug.Status) server.DriverStatus {
	s := server.DriverStatus{
		Kind:      "sparkplug",
		Name:      st.Group + "/" + st.EdgeNode,
		Detail:    st.Broker,
		SinceMs:   st.BornMs,
		LastError: "",
	}
	// State machine over the real Sparkplug flags — the human sentence
	// mirrors tentacle's store-forward banner reasoning.
	switch {
	case !st.Connected:
		s.State = "error"
		s.Message = "Broker unreachable"
		s.SinceMs = 0
	case st.PrimaryHost != "" && !st.HostOnline:
		s.State = "waiting"
		if st.PrimaryHostSeen {
			s.Message = fmt.Sprintf("Waiting for primary host %q (offline)", st.PrimaryHost)
		} else {
			s.Message = fmt.Sprintf("Waiting for primary host %q to announce", st.PrimaryHost)
		}
	case !st.Born:
		s.State = "connecting"
		s.Message = "Connected, publishing birth"
	default:
		s.State = "connected"
		s.Message = fmt.Sprintf("Publishing · bdSeq %d", st.BdSeq)
	}
	s.Metrics = []server.DriverMetric{
		{Label: "messages", Value: float64(st.Msgs)},
		{Label: "bdSeq", Value: float64(st.BdSeq)},
		{Label: "seq", Value: float64(st.Seq)},
	}
	if st.LastPubMs > 0 {
		s.Metrics = append(s.Metrics, server.DriverMetric{Label: "last publish", Text: agoText(st.LastPubMs)})
	}
	if sf := st.StoreForward; sf != nil {
		s.Metrics = append(s.Metrics, server.DriverMetric{Label: "buffered", Value: float64(sf.Buffered), Text: fmt.Sprintf("%d / %d", sf.Buffered, sf.Max)})
		if sf.Buffered > 0 && s.State == "connected" {
			s.State = "degraded"
			s.Message = fmt.Sprintf("Draining %d buffered messages", sf.Buffered)
		}
	}
	for _, d := range st.Devices {
		detail := "offline"
		if d.Online {
			detail = fmt.Sprintf("%d tags", d.Tags)
		}
		s.Devices = append(s.Devices, server.DriverDevice{ID: d.ID, Online: d.Online, Detail: detail})
	}
	s.Extra = map[string]any{
		"born":        st.Born,
		"primaryHost": st.PrimaryHost,
	}
	return s
}

// agoText renders an epoch-ms timestamp as a COMPACT, fixed-width-ish
// freshness for a status tile — no "ago" (the tile's LAST POLL / LAST
// PUBLISH label already means that), so it fits one line and never wraps
// (a wrapping value would reflow the card every time it ticked).
func agoText(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds()) // 0.4s
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds()) // 42s
	case d < time.Hour:
		return fmt.Sprintf("%.0fm", d.Minutes()) // 5m
	default:
		return fmt.Sprintf("%.0fh", d.Hours()) // 2h
	}
}
