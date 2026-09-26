package main

import (
	"math"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// Plant is an in-process simulation of a heated surge tank — one tank, one
// pump, one heater — implemented as a nautilus io.Driver: it consumes the
// controller's outputs (PumpRun, Heater) and produces the field inputs
// (LevelPct, TempC). This is the shape a real field-bus driver has too
// (Modbus, EtherNet/IP, OPC-UA): ReadInputs reports the transmitters,
// WriteOutputs applies the commands, and the runtime never knows the
// difference between this and a socket.
type Plant struct {
	mu        sync.Mutex
	volumeL   float64
	tempC     float64
	pumpRun   bool
	heaterPct float64
	last      time.Time
	clk       clock
	frozen    bool
}

// clock abstracts "now" so program_test.go can drive this Euler integration
// on the same virtual clock the acceptance scheduler advances, instead of
// the real wall-clock microseconds a test loop actually takes. Production
// never sets it — NewPlant defaults to the wall clock.
type clock interface{ Now() time.Time }

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

const (
	capacityL   = 2400.0
	pumpLps     = 2.5   // inflow when the pump runs
	demandLps   = 1.0   // steady outflow
	inletTempC  = 15.0  // cold supply water
	heaterKW    = 240.0 // full heater bank
	cpJPerKgK   = 4186.0
	ambientLoss = 0.0008 // loss coefficient toward ambient (per °C, per s)
	ambientC    = 20.0
)

func NewPlant() *Plant {
	return &Plant{volumeL: capacityL * 0.6, tempC: 60, clk: wallClock{}}
}

// WithClock swaps the plant's time source. Test-only: main.go never calls
// this, so production always integrates against the wall clock.
func (p *Plant) WithClock(c clock) *Plant {
	p.clk = c
	return p
}

// Freeze pins the level (and so the pump's hysteresis, and the cold-inflow
// mixing it would otherwise inject into the thermal balance) at levelPct,
// isolating the temperature loop for a test. Test-only.
func (p *Plant) Freeze(levelPct float64) *Plant {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.volumeL, p.frozen = capacityL*levelPct/100, true
	return p
}

// WriteOutputs receives the controller's commands.
func (p *Plant) WriteOutputs(v nio.Values) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := v["PumpRun"].(bool); ok {
		p.pumpRun = b
	}
	if h, ok := v["Heater"].(float64); ok {
		p.heaterPct = h
	}
	return nil
}

// ReadInputs steps the physics by the elapsed time and reports the transmitters.
func (p *Plant) ReadInputs() (nio.Values, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.clk.Now()
	dt := 0.1
	if !p.last.IsZero() {
		dt = math.Min(now.Sub(p.last).Seconds(), 0.5) // cap so a hitch can't blow up Euler
	}
	p.last = now

	inflow := 0.0
	if p.pumpRun && !p.frozen {
		inflow = pumpLps
	}
	if !p.frozen {
		// mass balance (1 L ≈ 1 kg)
		p.volumeL = clamp(p.volumeL+(inflow-demandLps)*dt, 0, capacityL)
	}

	// energy balance: heater in, ambient loss, cold-inflow mixing
	massKg := math.Max(p.volumeL, 1)
	qHeat := p.heaterPct / 100 * heaterKW * 1000 // W
	qMix := inflow * cpJPerKgK * (inletTempC - p.tempC)
	p.tempC += (qHeat+qMix)/(massKg*cpJPerKgK)*dt - ambientLoss*(p.tempC-ambientC)*dt
	p.tempC = clamp(p.tempC, 0, 110)

	return nio.Values{
		"LevelPct": p.volumeL / capacityL * 100,
		"TempC":    p.tempC,
	}, nil
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
