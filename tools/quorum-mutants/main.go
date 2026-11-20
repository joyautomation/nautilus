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
	{"answer_before_fsync", []string{"TestVoterGrantsOnlyAfterPersist", "TestTargetedFsyncBeforeAnswer", "TestGrantLeavesOnlyAfterTheWrite"}},
	{"lease_from_reply", []string{"TestCandidateLeaseFromSendMinusRho", "TestSkewWithinRhoHoldsBeyondBreaks"}},
	{"no_backoff", []string{"TestCandidateBackOffAfterLapse", "TestTargetedOneWayLeaderBacksOff"}},
	// Since the restart hold, TestTargetedOneWayLeaderSkewed's restarted
	// controller campaigns a lease later and no longer reaches this path.
	{"one_lease_backoff", []string{"TestCandidateBackOffAfterLapse", "TestTargetedOneWayLeaderBacksOff"}},
	{"any_incarnation", []string{"TestCandidateWitnessIncarnation", "TestMustFailSecondWitnessCopy", "TestMustFailSwitchQuietPeriod", "TestSeeds"}},
	{"any_sender", []string{"TestCandidateCountsOnlyItsVoters", "TestMustFailUnknownSender"}},
	{"no_grantee_check", []string{"TestCandidateGranteeCheckedEvenOnANonceCollision", "TestMustFailGrantMeantForThePeer"}},
	{"no_nonce_check", []string{"TestCandidateIgnoresAGrantFromAnEarlierBoot", "TestMustFailPreRestartGrant"}},
	{"match_by_term", []string{"TestCandidateGrantCountsOnlyForItsRequest", "TestTargetedGrantMatchedByRequest"}},
	// Since #979 the re-term and the yield bring a standby that doesn't see
	// refused renewals back to the leader too, so only the unit test
	// catches this one (test plan §3.3).
	{"renewals_unseen", []string{"TestVoterLeaderSeen"}},
	{"no_reterm", []string{"TestStandbyRejoinsTheLeader", "TestMatrix", "TestSeeds"}},
	// Since the handshake (H2) the two livelock seeds re-adopt the witness
	// and elect with three voters, so only T1 pins the yield.
	{"no_yield", []string{"TestTwoCountedVotersElect", "TestYieldCancelsOpenRounds"}},
	{"no_observe", []string{"TestObserveLiftsTheNextCampaign"}},
	{"retry_on_grid", []string{"TestCandidateRetryJitter"}},
	// Since the restart hold the nightly slow-fsync seed's first election
	// moves and no longer depends on it: T1 pins it.
	{"renew_on_look", []string{"TestCandidateRenewsAtOnceOnWinning"}},
	// The witness-incarnation handshake (#976): T1 since H1, the sim since H2.
	{"no_quiet", []string{"TestRebuiltWitnessAdopted", "TestQuietPeriodHoldsOffTheNewWitness", "TestLostControllerReadoptsThePeer",
		"TestReadoptOverPendingOwesTheQuietPeriod", "TestSecondAdoptionInFlightKeepsTheQuiet",
		"TestMustFailSwitchQuietPeriod", "TestSeeds"}},
	// Since #710 B2 the random both-lost runs wait for the operator, so
	// the seeds no longer re-commission on a floor: T1 and the named case.
	{"no_floor", []string{"TestRebuiltWitnessAdopted", "TestLostControllerReadoptsThePeer", "TestFloorCarriesTheUnseenTerm"}},
	{"count_while_pending", []string{"TestRebuiltWitnessAdopted", "TestReadoptOverPendingOwesTheQuietPeriod", "TestSeeds"}},
	{"no_adopt_gen", []string{"TestStaleProposalRefused", "TestConcurrentProposals", "TestLateProposalCannotSplitThePair",
		"TestLateEarlierBootProposalRefused"}},
	{"no_holdoff", []string{"TestSwitchHoldOffGrows", "TestFlappingStrayBounded"}},
	// Since the restart hold a restarted controller doesn't campaign for
	// the lease its quiet period covers, so counting at once changes
	// nothing it does (the hold is a second guard on the same window): the
	// sim no longer sees it, and T1 pins the count's own rule.
	{"restart_counts_at_once", []string{"TestRestartOwesTheQuietPeriod", "TestReviewRestartSkipsQuiet"}},
	{"no_adopt_echo", []string{"TestLostControllerIgnoresAnAckToItsEarlierBoot", "TestReviewStaleProposeFloor"}},
	{"switch_without_peer", []string{"TestSwitchNeedsThePeer"}},
	{"peer_from_any_adopt", []string{"TestLateAdoptToAnEarlierBootIsNotThePeer", "TestLateAdoptToAnEarlierBootInTheSim"}},
	{"bump_cuts_quiet", []string{"TestGenBumpKeepsTheRestartQuiet"}},
	// Since the restart hold, TestTargetedOneWayLeaderSkewed no longer
	// reaches it (see one_lease_backoff): T1 pins it.
	{"quiet_reply_unheard", []string{"TestQuietReplyIsHeardNotCounted"}},
	{"uncommissioned_grants", []string{"TestUncommissionedControllerGrantsNothing", "TestRegression976WitnessUnreachable"}},
	// #1014 review: belt and braces against counting an adoption while a
	// proposal is pending. Either rule alone closes the failed- and
	// slow-write split brain (TestReviewFailedWriteCountsWhilePending,
	// TestReviewSlowWriteCountsWhilePending, red with both removed), so
	// each is pinned by a test of its own invariant.
	{"propose_keeps_commit", []string{"TestProposeDropsAnAdoptionInFlight"}},
	{"count_over_pending", []string{"TestNoCountWhilePending"}},
	{"holdoff_from_boot", []string{"TestHoldOffFromAFailedAdoption"}},
	// The two-of-three case waits for the operator (#710 (B)). With this
	// switch on, the 200,000 seeds still pass, I2/I4 asserted and I4 also
	// flagging a term re-led after a restart: no random schedule has the
	// lost controller lead a term its peer never saw. So the named sim case
	// builds one, and TestSeeds is not in the row.
	{"uncommissioned_takes_switch", []string{"TestUncommissionedRefusesASwitch", "TestUncommissionedAlarmsOnADeadIncarnation",
		"TestRecommissionByHand", "TestHeardCommittedIncarnationRaisesNoAlarm", "TestTwoOfThreeWaitsForTheOperator"}},
	{"proposal_floor_volatile", []string{"TestProposalFloorSurvivesARestart"}},
	// #710 B2: both controllers uncommissioned wait for the operator too.
	{"both_lost_recommission", []string{"TestBothLostWaitForTheOperator", "TestReviewBothLostWitnessIntact",
		"TestBothLostWaitForTheOperatorInTheSim", "TestRegression976TwoLostControllerFiles",
		"TestRegressionBothUncommissionedStray", "TestRegression976WitnessUnreachable"}},
	{"alarm_on_heard", []string{"TestHeardCommittedIncarnationRaisesNoAlarm"}},
	{"recommission_any_floor", []string{"TestRecommissionFloorAboveTheKnown"}},
	// The driver, quorum/node (#710 PR 3a): the real transport, file and
	// clock. Its loopback tests skip under a core mutation (above).
	{"gate_on_tick", []string{"TestGateJudgedWhenRead"}},
	{"peer_host_trusted", []string{"TestGuardRequiresThePeerToken"}},
	{"persisted_before_write", []string{"TestGrantLeavesOnlyAfterTheWrite"}},
	{"state_in_place", []string{"TestStateCrashMidWriteKeepsTheOld"}},
	{"nonce_fixed", []string{"TestBootNonceDrawnPerStartNeverPersisted"}},
	{"lease_unchecked", []string{"TestVoteLeaseLongerThanThePromiseRefused", "TestReviewWitnessShorterLeaseRestart"}},
	{"witness_rebuilds_silently", []string{"TestWitnessRefusesUnreadableStateWithoutRebuild"}},
	// The #1021 review: a restart with a shorter lease (core and driver),
	// and the driver rules no test pinned.
	{"restart_wait_current_lease", []string{"TestVoterRestartWaitsTheLongerLease", "TestReviewWitnessShorterLeaseRestart"}},
	// The restart hold. TestLoopbackRestartedStandbyHoldsItsCampaigns
	// catches it too, but the loopback tests skip under a core mutation
	// (QUORUM_LOOPBACK=1 runs them).
	{"campaign_in_restart_wait", []string{"TestRestartedStandbyHoldsItsCampaigns", "TestCampaignHoldEndsWithTheRestartWait"}},
	{"controller_lease_unchecked", []string{"TestReviewControllerRefusesALongerLease"}},
	{"controller_write_ignored", []string{"TestReviewControllerFailedWriteSendsNoGrant"}},
	{"witness_write_ignored", []string{"TestReviewWitnessFailedWriteSendsNoGrant"}},
	{"gate_ignores_health", []string{"TestLoopbackUnhealthyLeaderClosesItsGate", "TestLoopbackReleaseClosesTheGate"}},
	{"controller_unlocked", []string{"TestReviewControllerStateLocked"}},
	{"witness_unlocked", []string{"TestReviewWitnessDirLocked"}},
	{"no_term_cap", []string{"TestReviewWitnessTermJumpRefused"}},
	{"controller_no_term_cap", []string{"TestReviewControllerTermJumpRefused"}},
	{"hello_unbound", []string{"TestReviewHelloReflectedAnswerFails"}},
	// The #1021 re-review.
	{"lease_raise_volatile", []string{"TestVoterLongerLeaseDurableBeforeAnyGrant", "TestReviewLengthenThenShorten"}},
	{"term_cap_narrow", []string{"TestReviewIsolatedControllerRejoins"}},
	// The #1028 review: two processes under one member's name.
	// cmd/naut's TestPairClonedMemberNeverLeadsTwice and
	// TestPairCopiedEnvWithKeptStateRefused catch them end to end too.
	{"duplicate_member_granted", []string{"TestVoterRefusesANameUnderAnotherBootNonce"}},
	{"state_owner_unchecked", []string{"TestStateFileBoundToItsOwner"}},
	// #710 PR 3b-2b: the hello's peer info (F30). cmd/naut's
	// TestPairConfigDiffersHoldsCampaigns catches the first end to end.
	{"config_differs_campaigns", []string{"TestHelloPeerInfo"}},
	{"hello_duplicate_unseen", []string{"TestHelloPeerInfo"}},
	// #708: the credential check's MAC must bind the digest.
	{"creds_unbound", []string{"TestCredentialCheck"}},
	{"creds_names_unbound", []string{"TestCredMACBindsNamesAndChallenge"}},
	{"creds_challenge_unbound", []string{"TestCredMACBindsNamesAndChallenge"}},
	// #1029: a cloned disk votes as the original. cmd/naut's
	// TestCloneWithTheABLinkDown and TestCloneWithACutFromTheWitness lead
	// twice under it too.
	{"box_unchecked", []string{"TestStateFromAnotherBoxIsLost"}},
	// #1034 review: a box that can't be read yet must hold the vote, never
	// skip the check. cmd/naut's TestCloneWithAnUnresolvableLinkNeverVotes
	// leads twice under it.
	{"box_wait_skipped", []string{"TestUnreadableBoxHoldsTheVoter"}},
	// Outside the quorum tree (rowPkgs): the operator-intent log's late-
	// start lapse (#1030, #1032 reviews) and the pair's alarm acks (#710
	// PR 3b-2c).
	{"lapse_ignored", []string{"TestLateUnknownStartLapses", "TestAdoptedCopyStillLapses", "TestUnconfirmedWriteOnlyTheWitnessStored"}},
	{"ack_clear_off", []string{"TestPairAckDoesNotCarryToANewOccurrence", "TestPairEndedAckDoesNotCarryAcrossATakeoverFromTheWitness"}},
	{"alarm_view_ignored", []string{"TestPairAckCarriesAcrossASyncedTakeover"}},
	// #1033 review: a re-leading replica's frozen engine.
	{"engine_not_reset", []string{"TestReviewFrozenEngineIsReset", "TestReviewFrozenShelfIsReset"}},
	// #1033 CI: the alarm log judged the engine before the takeover
	// restored it. cmd/naut's TestPairAckCarriesWhileTheTakeoverWaits
	// loses the ack under it too.
	{"reconcile_before_restore", []string{"TestAlarmLogWaitsForTheRestore"}},
	// #710 PR 3b-3a, design §3.4: writes leave in time, on every output
	// path. The four test plan §3.3 rows.
	{"no_fence_margin", []string{"TestGateClosesAFenceMarginBeforeTheLeaseEnd"}},
	{"gate_per_batch", []string{"TestGateClosedMidBatchStopsTheRest"}},
	// #1040 review: a command from an earlier term of the lease.
	{"old_term_sent", []string{"TestCommandsFromAnEarlierTermAreNotSent"}},
	{"send_no_deadline", []string{"TestBegin", "TestEachCommandRunsToItsSendDeadline", "TestModbusEachWriteRunsToItsSendDeadline", "TestModbusFencedWriteResetsTheSocketAtItsDeadline", "TestEIPEachRequestRunsToItsSendDeadline", "TestArmResetsAtTheDeadline", "TestModbusResetIsATimerAtTheDeadline", "TestFencedSendUnitDataResetIsATimer", "TestOptoMMPFencedWriteResetsAtItsDeadline"}},
	{"fence_fin", []string{"TestResetSendsAnRSTAndDropsQueuedBytes", "TestModbusFencedWriteResetsTheSocketAtItsDeadline", "TestFencedSendUnitDataResetsAtItsDeadline", "TestFencedPostResetsAtItsDeadline", "TestOptoMMPFencedWriteResetsAtItsDeadline", "TestSocketAbortAndCloseReset", "TestOPCUAFencedWriteResetsItsSession", "TestOptoMMPRTFencedWriteAbortsInPlaceAndClosesOnce"}},
	// 3b-3b-2: a fragmented channel write reads the gate before each request.
	{"fragments_unchecked", []string{"TestPairChannelWriteRunsFencedWithItsGate", "TestSendUnitDataChecksTheGateBeforeEachRequest", "TestFragmentedWriteStopsWhenTheGateCloses"}},
	// #1040 review: a Modbus write queued or requeued under an earlier
	// term of the lease.
	{"old_term_kept", []string{"TestModbusRequeuedWriteDoesNotSurviveAReLead", "TestOptoMMPTermMoveDropsTheQueue", "TestHeldCommandFromAnEarlierTermIsNotPublished", "TestOPCUAQueuedWriteDoesNotSurviveATermMove", "TestTermMoveSeenByAnotherCallerStopsTheFlush", "TestFailedPublishAfterATermMoveIsNotRequeued", "TestOPCUAFlushRacingAReopenSendsNothing", "TestOPCUAFailedChunkNotRequeuedAfterAReopen"}},
	// #1040 review: an SNMP SET retransmitted after a lost answer.
	{"set_retried", []string{"TestSetIsSentOnce"}},
	// #710 PR 3b-4a: the operator-intent log on three replicas. The
	// three-replica simulation (TestThreeReplicaSimulation, 120 seeds)
	// also catches the three safety rows; it is not listed, so a row
	// doesn't hang on one seed's schedule.
	{"replicate_sequential", []string{"TestASilentPeerDoesNotHoldUpWritesOrCatchUp"}},
	{"admit_skipped", []string{"TestAdmitDecidesWhoMayReplicate"}},
	{"unsynced_counted", []string{"TestEntryAckedByAAndWSurvivesWLosingItsLog", "TestLostLogUnderTheHeldMarkComesBackNonCounting", "TestRebuiltWitnessDoesNotCountUntilSynced", "TestOnlyASyncedLeaderSyncsAFollower"}},
	{"log_loss_silent", []string{"TestEntryAckedByAAndWSurvivesWLosingItsLog", "TestLostLogUnderTheHeldMarkComesBackNonCounting", "TestOnlyASyncedLeaderSyncsAFollower"}},
	{"synced_by_unsynced_leader", []string{"TestFollowerSyncsOnlyFromASyncedLeadersRequest"}},
	{"resend_unchunked", []string{"TestResendAndCatchUpGoInChunks"}},
	// #1050 review.
	{"unsynced_leader_writes", []string{"TestUnsyncedLeaderHeartbeatDoesNotOverwriteACommittedEntry", "TestFollowerTakesNothingFromAnUnsyncedLeader"}},
	{"behind_counted", []string{"TestAPeerStillBehindDoesNotCount"}},
	{"synced_before_commit", []string{"TestFirstChunkDoesNotSyncARebuiltReplica"}},
	{"unsynced_self_counted", []string{"TestOnlyASyncedLeaderSyncsAFollower"}},
	{"entry_unbounded", []string{"TestTooLargeEntryIsRefusedAtProposal"}},
	// #710 PR 3b-4b: the witness as the log's third copy, wired into the
	// pair. In the elector (quorum/node): the unsynced controller's
	// preference and yield.
	{"campaign_ignores_sync", []string{"TestLoopbackUnsyncedStandbyDefersTwoLeasesAtMost"}},
	{"unsynced_leader_keeps_lead", []string{"TestLoopbackUnsyncedLeaderYieldsToASyncedPeer"}},
	{"yield_undamped", []string{"TestLoopbackYieldIsDamped"}},
	{"yield_ignores_may", []string{"TestLoopbackNoYieldToAnUnhealthyPeer"}},
	{"yield_ignores_can_sync", []string{"TestLoopbackNoYieldToAPeerThatCannotSync"}},
	{"yield_until_due", []string{"TestYieldHoldsUntilTheLeaseIsSeenOver"}},
	// In the log (oplog).
	{"term_not_persisted", []string{"TestLostLogKeepsItsTerm"}},
	{"unnamed_leader_admitted", []string{"TestMarkedReplicaRefusesAnUnnamedLeader"}},
	{"vouch_unchecked", []string{"TestVouchRefuses"}},
	{"vouch_alone_kept", []string{"TestVouchedCatchUpAloneIsOnce", "TestPairCommissioningVouchSyncsTheOtherCopies"}},
	{"mark_after_log", []string{"TestMarkIsWrittenBeforeTheLog"}},
	// #1053 review.
	{"vouch_alone_partial", []string{"TestDoubleVouchWithASilentWitnessKeepsTheWrite"}},
	{"vouch_alone_ignores_ahead", []string{"TestVouchedCatchUpWaitsForALongerCopyToCount"}},
	{"vouch_refuses_counting_ahead", []string{"TestVouchOverACountingLongerWitnessRecoversItsEntries", "TestPairBothControllersLostRecoverFromTheWitness"}},
	{"vouch_refusal_blocks", []string{"TestPairRefusedVouchStillCommissions"}},
	{"can_sync_stale", []string{"TestCanSyncNeedsACopyHeardWithinTheBound"}},
	{"placeholder_keeps_synced", []string{"TestRebindAPendingReplica"}},
	{"unsynced_log_writes", []string{"TestFollowerOfAnUnsyncedLeaderIsNotWritable", "TestPairUnsyncedTakeoverCarriesNoAcks"}},
	// In the pair's wiring (cmd/naut), end to end.
	{"witness_log_unbound", []string{"TestPairWitnessLogAdmitsOnlyItsGrantedLeader"}},
	{"witness_log_term_unbound", []string{"TestPairWitnessLogAdmitsOnlyItsGrantedLeader", "TestWitnessAdmitsAtOrBelowItsPromise"}},
	{"witness_log_any_incarnation", []string{"TestWitnessCountsOnlyAtTheCountedIncarnation"}},
	{"witness_not_a_replica", []string{"TestPairLogWritesWithItsPeerDown", "TestPairLogSurvivesALeaderCrash", "TestPairAckCarriesAcrossATakeoverAfterTheLeaderDied"}},
	// The retry is also what finds a leader's term stuck, which the yield
	// waits on.
	{"log_sync_not_retried", []string{"TestPairUnsyncedLeaderYieldsToAVouchedPeer"}},
}

// rowPkgs are the packages a row runs instead of ./quorum/..., filtered
// to its tests: a switch outside the quorum tree.
var rowPkgs = map[string][]string{
	"lapse_ignored":                {"./oplog"},
	"replicate_sequential":         {"./oplog"},
	"admit_skipped":                {"./oplog"},
	"unsynced_counted":             {"./oplog"},
	"log_loss_silent":              {"./oplog"},
	"synced_by_unsynced_leader":    {"./oplog"},
	"resend_unchunked":             {"./oplog"},
	"unsynced_leader_writes":       {"./oplog"},
	"behind_counted":               {"./oplog"},
	"synced_before_commit":         {"./oplog"},
	"unsynced_self_counted":        {"./oplog"},
	"entry_unbounded":              {"./oplog"},
	"term_not_persisted":           {"./oplog"},
	"unnamed_leader_admitted":      {"./oplog"},
	"vouch_unchecked":              {"./oplog"},
	"vouch_alone_kept":             {"./oplog", "./cmd/naut"},
	"mark_after_log":               {"./oplog"},
	"vouch_alone_partial":          {"./oplog"},
	"vouch_alone_ignores_ahead":    {"./oplog"},
	"vouch_refuses_counting_ahead": {"./oplog", "./cmd/naut"},
	"vouch_refusal_blocks":         {"./cmd/naut"},
	"can_sync_stale":               {"./oplog"},
	"placeholder_keeps_synced":     {"./oplog"},
	"unsynced_log_writes":          {"./oplog", "./cmd/naut"},
	"witness_log_unbound":          {"./cmd/naut"},
	"witness_log_term_unbound":     {"./cmd/naut"},
	"witness_log_any_incarnation":  {"./cmd/naut"},
	"witness_not_a_replica":        {"./cmd/naut"},
	"log_sync_not_retried":         {"./cmd/naut"},
	"ack_clear_off":                {"./cmd/naut"},
	"alarm_view_ignored":           {"./cmd/naut"},
	"engine_not_reset":             {"./server"},
	"reconcile_before_restore":     {"./server"},
	"gate_per_batch":               {"./hw"},
	"old_term_sent":                {"./hw"},
	"send_no_deadline":             {"./internal/fence", "./hw", "./modbus", "./eip", "./eip/cip", "./optommp"},
	"fence_fin":                    {"./internal/fence", "./modbus", "./eip/cip", "./redfish", "./optommp", "./opcua"},
	"fragments_unchecked":          {"./runtime", "./eip/cip", "./eip"},
	"old_term_kept":                {"./modbus", "./optommp", "./sparkplug/host", "./opcua"},
	"set_retried":                  {"./snmp"},
}

// args are the go test arguments for m.
func (m mutation) args() []string {
	pkgs, ok := rowPkgs[m.name]
	if !ok {
		return []string{"./quorum/..."}
	}
	return append([]string{"-run", "^(" + strings.Join(m.expect, "|") + ")$"}, pkgs...)
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

func run(name string, args []string) []byte {
	cmd := exec.Command("go", append([]string{"test", "-tags", "quorum_mut", "-count=1", "-json"}, args...)...)
	cmd.Env = append(os.Environ(), "QUORUM_MUT="+name)
	out, _ := cmd.CombinedOutput() // a red run exits non-zero; the stream says why
	return out
}

func main() {
	only := flag.String("mut", "", "run only this mutation")
	flag.Parse()
	ok := true
	fmt.Println("| mutation | result |\n|---|---|")
	b, why := baseline(parse(run("", mutation{}.args())))
	for _, m := range table {
		if _, out := rowPkgs[m.name]; out && b {
			b, why = baseline(parse(run("", m.args()))) // the rows outside the tree, green first
		}
	}
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
		caught, why := verdict(m, parse(run(m.name, m.args())))
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
