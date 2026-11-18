package optommp

import (
	"math"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
)

// The fake's hooks make one channel read Bad while the link stays up (the
// driver contract suite's C3, #885), and only that channel: a digital
// channel through its quality bit, an analog one through NaN, in both
// read modes.
func TestFakeHooksMakeOneChannelBad(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "packed"
		if custom {
			name = "custom-area"
		}
		t.Run(name, func(t *testing.T) {
			dev := newBenchDevice()
			drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{CustomArea: custom}))
			eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
			q := func(tag string) nio.Quality { return drv.Quality()[tag] }
			linkUp := func(what string) {
				t.Helper()
				if h := drv.Health(); !h.Connected {
					t.Errorf("%s: link down (%+v)", what, h)
				}
			}

			dev.SetChannelBad(0, 7, true)
			eventually(t, "DI07 Bad", func() bool { return q("EPIC_M0_DI07") == nio.Bad })
			linkUp("DI07 Bad")
			if got := q("EPIC_M0_DI06"); got != nio.Good {
				t.Errorf("DI06 = %s, only channel 7 is flagged", got)
			}
			dev.SetChannelBad(0, 6, true)
			dev.SetChannelBad(0, 7, false)
			eventually(t, "DI07 Good, DI06 Bad", func() bool {
				return q("EPIC_M0_DI07") == nio.Good && q("EPIC_M0_DI06") == nio.Bad
			})

			dev.SetAnalog(3, 5, 4.25)
			eventually(t, "AI05 Good", func() bool { return q("EPIC_M3_AI05") == nio.Good })
			if f := value(t, drv, "EPIC_M3_AI05").(float64); f != 4.25 {
				t.Errorf("AI05 = %g", f)
			}
			dev.SetAnalog(3, 5, float32(math.NaN()))
			eventually(t, "AI05 Bad", func() bool { return q("EPIC_M3_AI05") == nio.Bad })
			linkUp("AI05 Bad")
			if got := q("EPIC_M3_AI00"); got != nio.Good {
				t.Errorf("AI00 = %s, only channel 5 is NaN", got)
			}
			dev.SetAnalog(3, 5, -1.5)
			eventually(t, "AI05 Good again", func() bool { return q("EPIC_M3_AI05") == nio.Good })
		})
	}
}

func TestFakeHooksRejectAChannelOffTheMap(t *testing.T) {
	dev := newBenchDevice()
	for _, call := range []func(){
		func() { dev.SetChannelBad(16, 0, true) },
		func() { dev.SetChannelBad(0, 32, true) },
		func() { dev.SetAnalog(-1, 0, 1) },
		func() { dev.SetAnalog(0, -1, 1) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("no panic for a channel off the memory map")
				}
			}()
			call()
		}()
	}
}
