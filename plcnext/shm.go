package plcnext

// shm.go is the `transport: shm` route for the image: the exchange page
// rt/plcnext-io/nautilus_io maps beside the firmware, where the shim
// copies the Axioline frames every cycle (1 ms) through the ANSI-C API.
// The layout is rt/plcnext-io/nautilus_io_shm.h — the one definition;
// shm_test.go fails if these numbers drift from it. Two seqlock blocks,
// one writer each: the shim's inputs block (input frame, the bytes it put
// in the output frame, the bus RUN bit, stamps, lateness) and this
// driver's outputs block (the bytes it owns, a stamp, a generation, the
// watchdog). Neither side ever waits on the other.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"unsafe"
)

// DefaultShm is where the shim creates the page.
const DefaultShm = "/dev/shm/nautilus-plcnext-io"

// The page, as nautilus_io_shm.h defines it.
const (
	shmSize     = 4096
	shmMagic    = 0x4E415554504E494F // "NAUTPNIO"
	shmVersion  = 1
	shmFrameLen = 512

	shmOffMagic     = 0
	shmOffVersion   = 8
	shmOffPeriodNs  = 12
	shmOffFrameLen  = 16
	shmOffPID       = 20
	shmOffStartedNs = 24
	shmOffLateUs    = 32

	shmOffInSeq   = 64
	shmOffStatus  = 68
	shmOffStampNs = 72
	shmOffFrameNs = 80
	shmOffCycles  = 88
	shmOffLate    = 96
	shmOffWorstUs = 104
	shmOffLastUs  = 108
	shmOffApplied = 112
	shmOffTorn    = 120
	shmOffInputs  = 128
	shmOffEcho    = 640
	// After version 1, in space a version-1 shim leaves zero.
	shmOffHist       = 1152
	shmHistN         = 12
	shmOffWorstWinUs = 1248
	shmOffPrio       = 1252

	shmOffOutSeq     = 2048
	shmOffOutLen     = 2052
	shmOffOutNs      = 2056
	shmOffOutGen     = 2064
	shmOffWatchdogMs = 2072
	shmOffOutputs    = 2112

	shmStPLC      = 0x01
	shmStFrames   = 0x02
	shmStRunKnown = 0x04
	shmStRun      = 0x08
	shmStFIFO     = 0x10
	shmStWatchdog = 0x20
)

// ShimHistLeUs are the upper bounds of the shim's lateness histogram
// buckets (NIO_HIST_LE_US); the last bucket is everything over the last bound.
var ShimHistLeUs = [shmHistN - 1]uint32{10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000}

// shmStaleAfter is how old the shim's stamp may be before its frames are
// Stale: a hundred of its 1 ms cycles, well past the worst best-effort
// lateness measured on the bench (under 10 ms).
const shmStaleAfter = 100 * time.Millisecond

// ShimState is one consistent copy of the shim's side of the page, plus
// what this side last wrote — what `naut plcnext shm` prints and Health
// summarises.
type ShimState struct {
	PID        uint32
	PeriodNs   uint32
	LateUs     uint32 // the threshold Late counts against
	StartedNs  int64
	Status     uint32
	StampNs    int64 // the shim's last cycle, CLOCK_MONOTONIC
	FrameNs    int64 // its last input-frame read
	Cycles     uint64
	Late       uint64
	WorstUs    uint32
	LastUs     uint32
	Applied    uint64           // the outputs generation in the bus frame
	Torn       uint64           // cycles the shim found this side mid-write on every try
	Hist       [shmHistN]uint64 // cycles by lateness since start, buckets ShimHistLeUs; zero from an older shim
	WorstWinUs uint32           // worst lateness in the last completed 10 000-cycle window
	Prio       uint32           // the SCHED_FIFO priority asked for (granted when FIFO())
	Inputs     [shmFrameLen]byte
	Echo       [shmFrameLen]byte // what the shim wrote to the output frame

	OutLen     uint32
	OutNs      int64
	OutGen     uint64
	WatchdogMs uint32
}

// PLC is "the PLC domain is started and the shim touches the frames".
func (s ShimState) PLC() bool { return s.Status&shmStPLC != 0 }

// RunKnown is "the shim read the bus RUN bit this cycle"; Running is its value.
func (s ShimState) RunKnown() bool { return s.Status&shmStRunKnown != 0 }
func (s ShimState) Running() bool  { return s.Status&shmStRun != 0 }

// FIFO is "the shim's cycle runs SCHED_FIFO".
func (s ShimState) FIFO() bool { return s.Status&shmStFIFO != 0 }

// Watchdog is "the shim is driving the output frame to zero because this
// side's outputs went stale".
func (s ShimState) Watchdog() bool { return s.Status&shmStWatchdog != 0 }

// errShmTorn: the shim was mid-write every time this side looked. It
// writes for microseconds a millisecond, but a best-effort shim can be
// descheduled inside that window (the bench sees it now and then),
// so the driver keeps its last copy for the scan rather than failing it.
var errShmTorn = errors.New("plcnext: shm: the shim was mid-write on every try")

// shmPage is a mapping of the page.
type shmPage struct {
	mem   []byte
	unmap func() error
	gen   uint64
}

func (p *shmPage) Close() error { return p.unmap() }

func (p *shmPage) u32(off int) uint32  { return binary.LittleEndian.Uint32(p.mem[off:]) }
func (p *shmPage) u64(off int) uint64  { return binary.LittleEndian.Uint64(p.mem[off:]) }
func (p *shmPage) seq(off int) *uint32 { return (*uint32)(unsafe.Pointer(&p.mem[off])) }

// header checks the page is the shim's: written, this version, this frame size.
func (p *shmPage) header() error {
	if m := atomic.LoadUint64((*uint64)(unsafe.Pointer(&p.mem[shmOffMagic]))); m != shmMagic {
		return fmt.Errorf("plcnext: shm: no nautilus_io header (magic %#x) — is the shim running?", m)
	}
	if v := p.u32(shmOffVersion); v != shmVersion {
		return fmt.Errorf("plcnext: shm: page version %d, this naut speaks %d — rebuild one side", v, shmVersion)
	}
	if n := p.u32(shmOffFrameLen); n != shmFrameLen {
		return fmt.Errorf("plcnext: shm: frame length %d, want %d", n, shmFrameLen)
	}
	return nil
}

// read copies the shim's block between two equal even sequence reads.
// The shim never stops for us, so a bounded number of tries; st changes
// only on success.
func (p *shmPage) read(out *ShimState) error {
	if err := p.header(); err != nil {
		return err
	}
	seq := p.seq(shmOffInSeq)
	var st ShimState
	for range 64 {
		s1 := atomic.LoadUint32(seq)
		if s1&1 == 1 {
			continue
		}
		st.Status = p.u32(shmOffStatus)
		st.StampNs = int64(p.u64(shmOffStampNs))
		st.FrameNs = int64(p.u64(shmOffFrameNs))
		st.Cycles = p.u64(shmOffCycles)
		st.Late = p.u64(shmOffLate)
		st.WorstUs = p.u32(shmOffWorstUs)
		st.LastUs = p.u32(shmOffLastUs)
		st.Applied = p.u64(shmOffApplied)
		st.Torn = p.u64(shmOffTorn)
		for i := range st.Hist {
			st.Hist[i] = p.u64(shmOffHist + 8*i)
		}
		st.WorstWinUs = p.u32(shmOffWorstWinUs)
		st.Prio = p.u32(shmOffPrio)
		copy(st.Inputs[:], p.mem[shmOffInputs:shmOffInputs+shmFrameLen])
		copy(st.Echo[:], p.mem[shmOffEcho:shmOffEcho+shmFrameLen])
		if atomic.LoadUint32(seq) != s1 {
			continue
		}
		st.PID = p.u32(shmOffPID)
		st.PeriodNs = p.u32(shmOffPeriodNs)
		st.LateUs = p.u32(shmOffLateUs)
		st.StartedNs = int64(p.u64(shmOffStartedNs))
		st.OutLen = p.u32(shmOffOutLen)
		st.OutNs = int64(p.u64(shmOffOutNs))
		st.OutGen = p.u64(shmOffOutGen)
		st.WatchdogMs = p.u32(shmOffWatchdogMs)
		*out = st
		return nil
	}
	return errShmTorn
}

// write publishes the output bytes this side owns (from offset 0) with a
// stamp, the next generation and the watchdog: odd, stores, even.
func (p *shmPage) write(out []byte, nowNs int64, watchdog time.Duration) {
	if len(out) > shmFrameLen {
		out = out[:shmFrameLen]
	}
	p.gen++
	seq := p.seq(shmOffOutSeq)
	s := atomic.LoadUint32(seq)
	if s&1 == 1 { // a writer before us died mid-write
		s++
	}
	atomic.StoreUint32(seq, s+1)
	binary.LittleEndian.PutUint32(p.mem[shmOffOutLen:], uint32(len(out)))
	binary.LittleEndian.PutUint64(p.mem[shmOffOutNs:], uint64(nowNs))
	binary.LittleEndian.PutUint64(p.mem[shmOffOutGen:], p.gen)
	binary.LittleEndian.PutUint32(p.mem[shmOffWatchdogMs:], uint32(watchdog/time.Millisecond))
	copy(p.mem[shmOffOutputs:], out)
	atomic.StoreUint32(seq, s+2)
}

// OpenShm maps the shim's page for `naut plcnext shm`: one look at it.
func OpenShm(path string) (*ShimView, error) {
	p, err := openShmPage(path)
	if err != nil {
		return nil, err
	}
	return &ShimView{p: p}, nil
}

// ShimView reads the page without ever writing it.
type ShimView struct{ p *shmPage }

// Read is one consistent copy, and how old the shim's stamp and input
// frame are by this process's monotonic clock.
func (v *ShimView) Read() (st ShimState, stampAge, frameAge time.Duration, err error) {
	if err = v.p.read(&st); err != nil {
		return st, 0, 0, err
	}
	now := monoNow()
	return st, time.Duration(now - st.StampNs), time.Duration(now - st.FrameNs), nil
}

func (v *ShimView) Close() error { return v.p.Close() }
