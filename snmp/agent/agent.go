// Package agent is the in-repo SNMP agent: a v2c UDP responder that answers
// Get, GetNext, GetBulk and Set from a recorded walk. It is the stand-in the
// driver tests poll on 127.0.0.1:0 and what `naut snmp serve` runs so a
// project with a switch in its manifest runs on a laptop with no switch
// (docs/design/it-drivers.md §8, the modbus/slave of this driver).
//
// It hand-writes no BER: requests are decoded with gosnmp's SnmpDecodePacket
// and answers encoded with SnmpPacket.MarshalMsg, the same codec the driver's
// client uses from the other end — so the agent can only disagree with the
// client where gosnmp disagrees with itself, and the foreign test (snmpsim)
// is what checks gosnmp against somebody else.
//
// Deliberately an agent of the simple kind: one community, v2c only (v3 is
// the foreign test's job), no views, no access control beyond "the OID
// exists and holds an INTEGER" for Set. A wrong community is silently
// dropped, exactly as net-snmp does it, so the driver's timeout path is the
// one exercised.
package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// maxBulkVarbinds bounds one GetBulk answer, the way a real agent bounds it
// by its maximum message size (a response that would not fit a datagram is
// cut short, not refused).
const maxBulkVarbinds = 60

// Agent serves one walk. Values are mutable (SetValue, Remove, or a Set
// request), so tests can step a counter or make a row vanish between polls.
type Agent struct {
	community string
	log       *slog.Logger

	mu       sync.Mutex
	w        walk.Walk
	conn     *net.UDPConn
	addr     string
	sets     []walk.Varbind
	requests map[string]int
	drop     bool
	done     chan struct{}
	ramps    map[string]ramp
}

// ramp makes a counter move with the wall clock: value = base + perSec ×
// seconds since Ramp — so a rate the driver computes across any two polls
// is perSec, whatever the poll timing.
type ramp struct {
	base   uint64
	perSec float64
	t0     time.Time
}

// New builds an agent over a copy of w answering to community.
func New(w walk.Walk, community string) *Agent {
	cp := append(walk.Walk(nil), w...)
	cp.Sort()
	return &Agent{community: community, w: cp, requests: map[string]int{}, ramps: map[string]ramp{}, log: slog.Default()}
}

// SetLogger replaces the logger (serve prints each Set it takes).
func (a *Agent) SetLogger(l *slog.Logger) {
	if l != nil {
		a.log = l
	}
}

// Start listens on addr ("127.0.0.1:0" for a free port). Start after Stop
// rebinds — pass Addr() to come back on the same port, which is how the
// driver tests take the agent down and bring it back.
func (a *Agent) Start(addr string) error {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	a.mu.Lock()
	a.conn = conn
	a.addr = conn.LocalAddr().String()
	a.done = done
	a.mu.Unlock()
	go a.serve(conn, done)
	return nil
}

// Addr is the bound address.
func (a *Agent) Addr() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.addr
}

// Stop closes the socket and waits for the loop.
func (a *Agent) Stop() {
	a.mu.Lock()
	conn, done := a.conn, a.done
	a.conn = nil
	a.mu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.Close()
	<-done
}

// SetDrop makes the agent swallow every request unanswered while on — the
// hung agent that holds the port and never replies.
func (a *Agent) SetDrop(on bool) {
	a.mu.Lock()
	a.drop = on
	a.mu.Unlock()
}

// SetValue replaces (or adds) one varbind.
func (a *Agent) SetValue(v walk.Varbind) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.ramps, v.OID)
	for i := range a.w {
		if a.w[i].OID == v.OID {
			a.w[i] = v
			return
		}
	}
	a.w = append(a.w, v)
	a.w.Sort()
}

// Ramp makes the counter at oid grow by perSec every second from its
// current value (Counter32 wraps at 2^32, as the real thing does). Ramping
// a counter that is already ramping changes its slope from where it has
// got to — never a step — so a plant can steer a rate second by second and
// the counter stays monotonic. A Set or SetValue on the OID stops the ramp.
func (a *Agent) Ramp(oid string, perSec float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.w.Get(oid)
	if !ok || (v.Type != walk.Counter32 && v.Type != walk.Counter64) {
		return fmt.Errorf("ramp %s: not a counter in the walk", oid)
	}
	a.ramps[oid] = ramp{base: a.current(v).Uint, perSec: max(perSec, 0), t0: time.Now()}
	return nil
}

// current applies a ramp to a varbind on its way out. Caller holds mu.
func (a *Agent) current(v walk.Varbind) walk.Varbind {
	r, ok := a.ramps[v.OID]
	if !ok {
		return v
	}
	v.Uint = r.base + uint64(r.perSec*time.Since(r.t0).Seconds())
	if v.Type == walk.Counter32 {
		v.Uint &= 1<<32 - 1
	}
	return v
}

// Remove deletes one OID: Get answers noSuchInstance for it from now on.
func (a *Agent) Remove(oid string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.w {
		if a.w[i].OID == oid {
			a.w = append(a.w[:i], a.w[i+1:]...)
			return
		}
	}
}

// Value returns the current varbind at oid.
func (a *Agent) Value(oid string) (walk.Varbind, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.w.Get(oid)
	return a.current(v), ok
}

// Sets returns every varbind a Set request applied, in order.
func (a *Agent) Sets() []walk.Varbind {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]walk.Varbind(nil), a.sets...)
}

// Requests counts answered PDUs by type ("get", "getnext", "getbulk", "set").
func (a *Agent) Requests() map[string]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]int, len(a.requests))
	for k, v := range a.requests {
		out[k] = v
	}
	return out
}

func (a *Agent) serve(conn *net.UDPConn, done chan struct{}) {
	defer close(done)
	dec := &gosnmp.GoSNMP{Version: gosnmp.Version2c, Community: a.community}
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		req, err := dec.SnmpDecodePacket(append([]byte(nil), buf[:n]...))
		if err != nil || req.Version != gosnmp.Version2c || req.Community != a.community {
			continue // undecodable, v1/v3, or the wrong community: silence, like net-snmp
		}
		a.mu.Lock()
		drop := a.drop
		a.mu.Unlock()
		if drop {
			continue
		}
		resp, err := a.answer(req)
		if err != nil {
			a.log.Warn("snmp agent: cannot answer", "error", err)
			continue
		}
		out, err := resp.MarshalMsg()
		if err != nil {
			a.log.Warn("snmp agent: cannot encode", "error", err)
			continue
		}
		_, _ = conn.WriteToUDP(out, from)
	}
}

func (a *Agent) answer(req *gosnmp.SnmpPacket) (*gosnmp.SnmpPacket, error) {
	resp := &gosnmp.SnmpPacket{
		Version:   gosnmp.Version2c,
		Community: req.Community,
		PDUType:   gosnmp.GetResponse,
		RequestID: req.RequestID,
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []walk.Varbind
	switch req.PDUType {
	case gosnmp.GetRequest:
		a.requests["get"]++
		for _, v := range req.Variables {
			oid := trim(v.Name)
			if vb, ok := a.w.Get(oid); ok {
				out = append(out, a.current(vb))
			} else {
				out = append(out, walk.Varbind{OID: oid, Type: walk.NoSuchInstance})
			}
		}
	case gosnmp.GetNextRequest:
		a.requests["getnext"]++
		for _, v := range req.Variables {
			out = append(out, a.next(trim(v.Name)))
		}
	case gosnmp.GetBulkRequest:
		a.requests["getbulk"]++
		nonRep := min(int(req.NonRepeaters), len(req.Variables))
		for _, v := range req.Variables[:nonRep] {
			out = append(out, a.next(trim(v.Name)))
		}
		cur := make([]string, 0, len(req.Variables)-nonRep)
		for _, v := range req.Variables[nonRep:] {
			cur = append(cur, trim(v.Name))
		}
		for r := 0; r < int(req.MaxRepetitions) && len(cur) > 0 && len(out)+len(cur) <= maxBulkVarbinds; r++ {
			allEnd := true
			for i, oid := range cur {
				vb := a.next(oid)
				out = append(out, vb)
				if vb.Type != walk.EndOfMibView {
					allEnd = false
					cur[i] = vb.OID
				}
			}
			if allEnd {
				break
			}
		}
	case gosnmp.SetRequest:
		a.requests["set"]++
		// Validate every varbind first, then apply all — RFC 3416's
		// all-or-nothing, in its simplest form.
		for i, v := range req.Variables {
			oid := trim(v.Name)
			cur, ok := a.w.Get(oid)
			in, err := snmp.FromPDU(v)
			switch {
			case err != nil:
				resp.Error, resp.ErrorIndex = gosnmp.WrongType, uint8(i+1)
			case !ok:
				resp.Error, resp.ErrorIndex = gosnmp.NoCreation, uint8(i+1)
			case cur.Type != in.Type:
				resp.Error, resp.ErrorIndex = gosnmp.WrongType, uint8(i+1)
			}
			if resp.Error != gosnmp.NoError {
				resp.Variables = req.Variables
				return resp, nil
			}
		}
		for _, v := range req.Variables {
			in, _ := snmp.FromPDU(v)
			delete(a.ramps, in.OID)
			for i := range a.w {
				if a.w[i].OID == in.OID {
					a.w[i] = in
				}
			}
			a.sets = append(a.sets, in)
			a.log.Info("snmp agent: set", "oid", in.OID, "value", in.ValueString())
		}
		resp.Variables = req.Variables
		return resp, nil
	default:
		return nil, fmt.Errorf("unsupported PDU %s", req.PDUType)
	}
	for _, vb := range out {
		p, err := snmp.ToPDU(vb)
		if err != nil {
			return nil, err
		}
		resp.Variables = append(resp.Variables, p)
	}
	return resp, nil
}

// next is GetNext over the walk; past the end it is endOfMibView at the
// requested OID. Caller holds mu.
func (a *Agent) next(oid string) walk.Varbind {
	if vb, ok := a.w.Next(oid); ok {
		return a.current(vb)
	}
	return walk.Varbind{OID: oid, Type: walk.EndOfMibView}
}

func trim(name string) string {
	if len(name) > 0 && name[0] == '.' {
		return name[1:]
	}
	return name
}
