package sim

import (
	"testing"
	"time"
)

// The 20 schedules the first 200,000-seed nightly failed on (#998), as
// named regression cases, by class (test plan §3.3).

func nightly(t *testing.T, seeds []uint64, run func(t *testing.T, o Options)) {
	t.Helper()
	for _, k := range seeds {
		o, ok := nightlySeeds[k]
		if !ok {
			t.Fatalf("seed %d has no schedule", k)
		}
		t.Run("seed"+itoa(k), func(t *testing.T) { run(t, o) })
	}
}

func itoa(k uint64) string {
	if k == 0 {
		return "0"
	}
	var b []byte
	for ; k > 0; k /= 10 {
		b = append([]byte{byte('0' + k%10)}, b...)
	}
	return string(b)
}

// Class 1, a liveness bug: a voter the majority needs fsyncs in 1.6–2.2 s
// (lease 2 s), so a won campaign's grants leave less of the lease than a
// look, and it lapsed before its first renewal: takeovers flapped (I7).
// Fixed by renewing at once on winning (Candidate.OnReply); each is red
// with the renew_on_look mutation.
func TestNightlySlowFsyncWinner(t *testing.T) {
	nightly(t, []uint64{52814, 57197, 129636, 142433, 162345, 176221, 184642, 193654}, func(t *testing.T, o Options) {
		for _, v := range Check(Run(o), checksFor(o)) {
			t.Error(v)
		}
	})
}

// Classes 3 and 4: a node lost its state file, wiped (30099 54172 84269)
// or torn by a crash during the witness's slow in-place write (21198). Until the handshake PR the witness can't back a controller that
// lost its accepted incarnation, nor is a rebuilt witness counted, so
// steady state is judged on A and B alone, from the record (#976). Every
// oracle holds then; red if the steady check still demands the witness.
func TestNightlyLostFileSteady(t *testing.T) {
	nightly(t, []uint64{21198, 30099, 54172, 84269}, func(t *testing.T, o Options) {
		rec := Run(o)
		if !rec.lostFile() {
			t.Fatal("no node lost its file: the case proves nothing")
		}
		for _, v := range Check(rec, checksFor(o)) {
			t.Error(v)
		}
	})
}

// Class 2, beyond the documented rule (lease ≥ 4 × the worst one-way
// vote-link delay): the witness wiped (so the pair depends on A–B) and an
// A–B delay of 0.86–0.96 s each way, lease/2.2. Takeovers may flap there
// (F21, measured); every other oracle, safety and steady state included,
// still holds. The generator no longer draws such delays
// (TestGeneratorDelayWithinRule).
func TestNightlyBeyondRuleDelay(t *testing.T) {
	nightly(t, []uint64{14523, 38969, 47591, 63039, 89414, 153106, 157046}, func(t *testing.T, o Options) {
		beyond := false
		for _, f := range o.Faults {
			if f.Kind == Delay && o.Delay+o.Jitter+time.Duration(f.Value) > lease/4 {
				beyond = true
			}
		}
		if !beyond {
			t.Fatal("no delay beyond lease/4: the case is misfiled")
		}
		c := checksFor(o)
		c.Exclude = []string{"I7"}
		for _, v := range Check(Run(o), c) {
			t.Error(v)
		}
	})
}

// Seed 61535 failed steady state on main because a crash during the
// witness's slow in-place write tore its file (class 4). Renewing at once
// shifts the run's timing so the crash no longer lands mid-write; with the
// renew_on_look mutation (main's timing) the file tears again and the
// lost-file rule carries it. Either way every oracle holds.
func TestNightlyTornWitnessTimingShifted(t *testing.T) {
	nightly(t, []uint64{61535}, func(t *testing.T, o Options) {
		for _, v := range Check(Run(o), checksFor(o)) {
			t.Error(v)
		}
	})
}

// The generator keeps every vote-link delay within the documented rule:
// base + jitter + fault ≤ lease/4.
func TestGeneratorDelayWithinRule(t *testing.T) {
	for seed := uint64(1); seed <= 20000; seed++ {
		o := randomRun(seed, profDefault)
		for _, f := range o.Faults {
			if f.Kind == Delay && o.Delay+o.Jitter+time.Duration(f.Value) > lease/4 {
				t.Fatalf("seed %d draws %v + %v + %v > lease/4", seed, o.Delay, o.Jitter, time.Duration(f.Value))
			}
		}
	}
}
