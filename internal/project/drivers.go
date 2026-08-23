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

	online, born := 0, 0
	for _, n := range st.Nodes {
		if n.Online {
			online++
		}
		if n.BirthMs > 0 {
			born++
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

	var lastMs int64
	for _, n := range st.Nodes {
		if n.LastMsgMs > lastMs {
			lastMs = n.LastMsgMs
		}
	}
	s.Metrics = []server.DriverMetric{
		{Label: "sites online", Value: float64(online), Text: fmt.Sprintf("%d / %d", online, len(st.Nodes))},
		{Label: "messages", Value: float64(st.Msgs)},
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
	if lastMs > 0 {
		s.Metrics = append(s.Metrics, server.DriverMetric{Label: "last message", Text: agoText(lastMs)})
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
	s.Extra = map[string]any{"nodes": st.Nodes}
	return s
}

// nodeDetail is one site's row text: "12 tags · born 3m" while it is up,
// and why it is not while it is down. Stale is called out separately from
// offline — a node that stopped talking without an NDEATH is a different
// fault (network, broker) from one that said goodbye.
func nodeDetail(n sphost.NodeStatus) string {
	switch {
	case !n.Online:
		return "offline"
	case n.Stale:
		return fmt.Sprintf("%d tags · stale %s", n.Metrics, agoText(n.LastMsgMs))
	case n.BirthMs > 0:
		return fmt.Sprintf("%d tags · born %s", n.Metrics, agoText(n.BirthMs))
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
