package ir

import "time"

// WallClock is an optional Host extension: the runtime's clock as a
// calendar instant, in the zone it should be read in. The production
// runtime answers the machine's local time; a runtime on a virtual clock
// (naut test) answers that clock, in UTC, so a test that reads the
// calendar is the same on every machine. A host without it is read from
// NowMs in the process's local zone.
type WallClock interface {
	Now() time.Time
}

func init() { registerLocalTime() }

// ─── LOCAL_TIME: the calendar, now ─────────────────────────────────
//
// Outputs: YEAR, MONTH (1–12), DAY (1–31), HOUR (0–23), MINUTE, SECOND,
// MILLISECOND — the names of IEC 61131-3's SPLIT_DT outputs. Each call
// reads the clock once, so the seven are one instant.

var localTimeOutputs = []string{"YEAR", "MONTH", "DAY", "HOUR", "MINUTE", "SECOND", "MILLISECOND"}

func registerLocalTime() {
	outs := make([]FBSlot, len(localTimeOutputs))
	for i, n := range localTimeOutputs {
		outs[i] = FBSlot{Name: n, Type: IntT}
	}
	RegisterFB(&FBDef{
		Name:    "LOCAL_TIME",
		Outputs: outs,
		Step: func(inst *FBInstance, ctx FBStepCtx) error {
			var now time.Time
			if wc, ok := ctx.Host.(WallClock); ok {
				now = wc.Now()
			} else {
				now = time.UnixMilli(ctx.NowMs)
			}
			inst.Slots[0] = IntVal(int64(now.Year()))
			inst.Slots[1] = IntVal(int64(now.Month()))
			inst.Slots[2] = IntVal(int64(now.Day()))
			inst.Slots[3] = IntVal(int64(now.Hour()))
			inst.Slots[4] = IntVal(int64(now.Minute()))
			inst.Slots[5] = IntVal(int64(now.Second()))
			inst.Slots[6] = IntVal(int64(now.Nanosecond() / int(time.Millisecond)))
			return nil
		},
	})
}
