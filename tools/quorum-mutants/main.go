// Command quorum-mutants runs the vote protocol's mutation checks (test
// plan §3.3): for each mutation in package quorum it runs the quorum tests
// with that one rule broken (build tag quorum_mut, QUORUM_MUT=<name>), and
// checks that the tests named for it go red. A mutation whose tests stay
// green has survived: the suite has a hole. A build or setup failure is a
// failure too, never a pass. The baseline (the tag on, no mutation) must
// be all green first.
//
//	go run ./tools/quorum-mutants            every mutation
//	go run ./tools/quorum-mutants -mut no_rho one
//
// It prints a Markdown table and exits non-zero if anything survived or
// failed to build.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// mutation is one row: the QUORUM_MUT name and the tests that must fail.
type mutation struct {
	name   string
	expect []string // top-level test names, any package under ./quorum/...
}

// The table. Each row names the tests that catch the rule, measured on the
// code and listed so a lost catch shows up as a failure here.
var table = []mutation{
	{"no_rho", []string{"TestCandidateLeaseFromSendMinusRho", "TestSkewWithinRhoHoldsBeyondBreaks", "TestSeeds"}},
	{"restart_grants", []string{"TestVoterRestartWait", "TestMustFailPreRestartGrant", "TestSeeds"}},
	{"answer_before_fsync", []string{"TestVoterGrantsOnlyAfterPersist", "TestTargetedFsyncBeforeAnswer"}},
	{"lease_from_reply", []string{"TestCandidateLeaseFromSendMinusRho", "TestSkewWithinRhoHoldsBeyondBreaks"}},
	{"no_backoff", []string{"TestCandidateBackOffAfterLapse", "TestTargetedOneWayLeaderBacksOff"}},
	{"one_lease_backoff", []string{"TestCandidateBackOffAfterLapse", "TestTargetedOneWayLeaderBacksOff", "TestTargetedOneWayLeaderSkewed"}},
	{"any_incarnation", []string{"TestCandidateWitnessIncarnation", "TestMustFailSecondWitnessCopy", "TestSeeds"}},
	{"any_sender", []string{"TestCandidateCountsOnlyItsVoters", "TestMustFailUnknownSender"}},
	{"no_grantee_check", []string{"TestCandidateGranteeCheckedEvenOnANonceCollision", "TestMustFailGrantMeantForThePeer"}},
	{"no_nonce_check", []string{"TestCandidateIgnoresAGrantFromAnEarlierBoot", "TestMustFailPreRestartGrant"}},
	{"match_by_term", []string{"TestCandidateGrantCountsOnlyForItsRequest", "TestTargetedGrantMatchedByRequest"}},
	// Since #979 the re-term and the yield bring a standby that doesn't see
	// refused renewals back to the leader too, so only the unit test
	// catches this one (test plan §3.3).
	{"renewals_unseen", []string{"TestVoterLeaderSeen"}},
	{"no_reterm", []string{"TestStandbyRejoinsTheLeader", "TestMatrix", "TestSeeds"}},
	{"no_yield", []string{"TestTwoCountedVotersElect", "TestRegressionTwoVoterLivelock"}},
	{"no_observe", []string{"TestObserveLiftsTheNextCampaign"}},
	{"retry_on_grid", []string{"TestCandidateRetryJitter"}},
	{"renew_on_look", []string{"TestCandidateRenewsAtOnceOnWinning", "TestNightlySlowFsyncWinner"}},
	// The witness-incarnation handshake (#976). H1 catches them in T1; the
	// sim cases join with H2.
	{"no_quiet", []string{"TestRebuiltWitnessAdopted", "TestQuietPeriodHoldsOffTheNewWitness", "TestLostControllerReadoptsThePeer"}},
	{"no_floor", []string{"TestRebuiltWitnessAdopted", "TestLostControllerReadoptsThePeer"}},
	{"count_while_pending", []string{"TestRebuiltWitnessAdopted"}},
	{"no_adopt_gen", []string{"TestStaleProposalRefused", "TestConcurrentProposals", "TestLateProposalCannotSplitThePair"}},
	{"no_holdoff", []string{"TestSwitchHoldOffGrows"}},
	{"restart_counts_at_once", []string{"TestRestartOwesTheQuietPeriod", "TestReviewRestartSkipsQuiet"}},
	{"no_adopt_echo", []string{"TestLostControllerIgnoresAnAckToItsEarlierBoot", "TestReviewStaleProposeFloor"}},
	{"switch_without_peer", []string{"TestSwitchNeedsThePeer"}},
	{"readopt_skips_quiet", []string{"TestReadoptOverPendingOwesTheQuietPeriod"}},
}

// event is one line of `go test -json`.
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// result is what one run did.
type result struct {
	failed   []string // top-level tests that failed
	broken   string   // a build or setup failure, or a package that failed with no test failing
	anyFails bool
	panicked bool // a test panicked: the tests after it in that package never ran
}

// parse reads a `go test -json` stream.
func parse(out []byte) result {
	var r result
	failedPkgTests := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		var e event
		if json.Unmarshal(line, &e) != nil {
			if s := string(line); strings.Contains(s, "build failed") || strings.Contains(s, "setup failed") {
				r.broken = strings.TrimSpace(s)
			}
			continue
		}
		if strings.HasPrefix(e.Output, "panic: ") {
			r.panicked = true
		}
		if strings.Contains(e.Output, "[build failed]") || strings.Contains(e.Output, "[setup failed]") {
			r.broken = strings.TrimSpace(e.Output)
		}
		if e.Action != "fail" {
			continue
		}
		r.anyFails = true
		switch {
		case e.Test == "":
			if !failedPkgTests[e.Package] && r.broken == "" {
				r.broken = "package " + e.Package + " failed with no test failing (a panic or a build error)"
			}
		case !strings.Contains(e.Test, "/"):
			failedPkgTests[e.Package] = true
			if !slices.Contains(r.failed, e.Test) {
				r.failed = append(r.failed, e.Test)
			}
		default:
			failedPkgTests[e.Package] = true
		}
	}
	return r
}

// verdict judges one mutation's run: caught (every expected test failed,
// nothing broken), or why not.
func verdict(m mutation, r result) (bool, string) {
	if r.broken != "" {
		return false, "BROKEN: " + r.broken
	}
	var missed []string
	for _, t := range m.expect {
		if !slices.Contains(r.failed, t) {
			missed = append(missed, t)
		}
	}
	note := ""
	if r.panicked {
		note = "; a test PANICKED, so later tests in its package never ran"
	}
	if len(missed) > 0 {
		return false, "SURVIVED: still green: " + strings.Join(missed, ", ") + note
	}
	return true, fmt.Sprintf("caught (%d tests red)%s", len(r.failed), note)
}

// baseline judges the run with the tag on and no mutation: all green.
func baseline(r result) (bool, string) {
	switch {
	case r.broken != "":
		return false, "BROKEN: " + r.broken
	case r.anyFails:
		return false, "baseline red: " + strings.Join(r.failed, ", ")
	}
	return true, "green"
}

func run(name string) []byte {
	cmd := exec.Command("go", "test", "-tags", "quorum_mut", "-count=1", "-json", "./quorum/...")
	cmd.Env = append(os.Environ(), "QUORUM_MUT="+name)
	out, _ := cmd.CombinedOutput() // a red run exits non-zero; the stream says why
	return out
}

func main() {
	only := flag.String("mut", "", "run only this mutation")
	flag.Parse()
	ok := true
	fmt.Println("| mutation | result |\n|---|---|")
	b, why := baseline(parse(run("")))
	fmt.Printf("| (baseline: the tag on, no mutation) | %s |\n", why)
	if !b {
		os.Exit(1)
	}
	found := false
	for _, m := range table {
		if *only != "" && m.name != *only {
			continue
		}
		found = true
		caught, why := verdict(m, parse(run(m.name)))
		ok = ok && caught
		fmt.Printf("| %s | %s |\n", m.name, why)
	}
	if !found {
		fmt.Fprintf(os.Stderr, "no mutation %q\n", *only)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}
