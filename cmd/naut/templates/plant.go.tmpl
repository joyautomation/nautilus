package main

import (
	"math"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// Plant is an in-process simulation of the heated surge tank, implemented as a
// nautilus io.Driver: it consumes the controller's outputs (pump, heater) and
// produces the field inputs (level, temperature). A real deployment swaps this
// for a Modbus/OPC-UA/etc. driver — the controller code is unchanged.
type Plant struct {
	mu        sync.Mutex
	volumeL   float64
	tempC     float64
	pumpRun   bool
	heaterPct float64
	last      time.Time
}

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
	return &Plant{volumeL: capacityL * 0.6, tempC: 60}
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

	now := time.Now()
	dt := 0.1
	if !p.last.IsZero() {
		dt = math.Min(now.Sub(p.last).Seconds(), 0.5) // cap so a hitch can't blow up Euler
	}
	p.last = now

	inflow := 0.0
	if p.pumpRun {
		inflow = pumpLps
	}
	// mass balance (1 L ≈ 1 kg)
	p.volumeL = clamp(p.volumeL+(inflow-demandLps)*dt, 0, capacityL)

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
