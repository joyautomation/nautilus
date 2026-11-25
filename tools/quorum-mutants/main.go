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
	{"hello_retry_slow", []string{"TestAHelloWithNoAnswerIsRetriedSoon"}},
	{"hello_not_asked_again", []string{"TestAPeersRestartAsksForAHelloAtOnce"}},
	{"hello_asks_unspaced", []string{"TestAsksForAHelloAreSpaced"}},
	// The #1021 re-review.
	{"lease_raise_volatile", []string{"TestVoterLongerLeaseDurableBeforeAnyGrant", "TestReviewLengthenThenShorten"}},
	{"term_cap_narrow", []string{"TestReviewIsolatedControllerRejoins"}},
	// The #1028 review: two processes under one member's name.
	// cmd/naut's TestPairClonedMemberNeverLeadsTwice and
	// TestPairCopiedEnvWithKeptStateRefused catch them end to end too.
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
	// #1029 layer 2: the voters relay their running promises, so a
	// MAC-spoofed clone (the box check can't see it) finds the voter it
	// needs refusing it. cmd/naut's TestSpoofedClone* lead twice under
	// relay_ignored (all three shapes), relay_voter_rule_off (the clone of
	// the standby) and standby_no_probe (both shapes cut from the witness).
	// Since #1029 step 1 the name refusal also stops every clone schedule
	// the relay rules did (TestCloneSeeds stays green under these): they
	// are pinned at the voter, where each still refuses a bad grant alone.
	{"relay_ignored", []string{"TestVoterRefusesANameAHeardPromiseHoldsUnderAnotherNonce", "TestVoterRefusesANameWhoseOwnVoterIsHeardPromisedElsewhere"}},
	{"relay_voter_rule_off", []string{"TestVoterRefusesANameWhoseOwnVoterIsHeardPromisedElsewhere"}},
	{"relay_self_promise", []string{"TestVoterRelaysOnlyAPromiseToAnotherNode"}},
	{"relay_no_margin", []string{"TestAHeardPromiseHoldsForItsMarginThenEnds"}},
	{"relay_tail_counted", []string{"TestAHeardPromiseHoldsForItsMarginThenEnds"}},
	{"standby_no_probe", []string{"TestStandbyProbesTheWitnessEveryLook", "TestCloneSeeds"}},
	{"relay_not_pushed", []string{"TestAPromiseToTheOtherControllerIsPushedToTheWitness"}},
	// #1029 step 1: a duplicated name gets no new grant. Each is pinned by
	// a schedule where it alone stands between the pair and two leaders
	// (quorum/sim/dupname_test.go), and the clone races catch most.
	// Since 3c-1 the mark also covers the step-1 sim pins (seed 8657's
	// race, the standby-probe schedule): those rules keep the pins below,
	// where each still alone stands between the pair and a fault.
	{"dup_name_ignored", []string{"TestADuplicatedNameGetsNoNewGrant", "TestVoterRefusesANameUnderAnotherBootNonce", "TestDupRelayStopsTheWitnessGrantingAClone"}},
	{"same_term_other_boot", []string{"TestATermIsNotGrantedToAnotherBoot", "TestATermStaysOneBootsAcrossARestart"}},
	{"grant_nonce_volatile", []string{"TestATermStaysOneBootsAcrossARestart"}},
	{"grant_nonce_unwritten", []string{"TestAnUpgradedVoterWritesTheNonceOnARenewal"}},
	{"dup_refuses_incumbent", []string{"TestADuplicatedNameGetsNoNewGrant", "TestCloneOfTheLeaderCostsNoTakeover"}},
	{"dup_voter_counts", []string{"TestCandidateDiscountsADuplicatedVoter", "TestCloneRaceSeeds"}}, // the split gate no longer: the spare silences the copy before its splits (3c-2)
	{"dup_not_relayed", []string{"TestADuplicatedNameIsRelayed", "TestDupRelayStopsTheWitnessGrantingAClone"}},
	{"standby_probes_witness_only", []string{"TestTheStandbysPeerProbeLetsItsPeerSeeAClone"}},
	{"yield_before_refusal", []string{"TestNoYieldToARequestTheVoterRefuses"}},
	{"dup_strict_ignores_delay", []string{"TestASingleRestartRaisesNoDuplicate"}},
	{"rule_b_counts_own_boot", []string{"TestRuleBCountsOnlyAnotherBoot", "TestASingleRestartRaisesNoDuplicate"}},
	// #1029 3c: a voter that heard two boots of a name running at once
	// marks it until cleared. The split gates are where the mark alone
	// stands between the pair and two leaders; the hold, where a split's
	// voters both restart.
	{"dup_mark_off", []string{"TestAMarkNeedsTheOldBootPastTheDelay", "TestAMarkedNameGetsNoNewGrantUntilCleared", "TestCloneSplitNoTwoLeaders", "TestCloneSplitVoterRestarts"}},
	{"dup_mark_counts_replays", []string{"TestAReplayedFrameNeverMarks", "TestLateCopiesAfterARestartMarkNothing"}},
	{"dup_mark_no_delay", []string{"TestAMarkNeedsTheOldBootPastTheDelay"}},
	{"dup_mark_one_sided", []string{"TestAVoterThatMissedTheOldBootMarksNoRestart", "TestRestartsBesideAVoterRestartMarkNothing"}},
	{"evicted_boot_banned", []string{"TestAnEvictedOriginalIsMarkedWhenItComesBack"}},
	// #1113 review, rules (i) and (ii): a hold ends only on the marking
	// voter boot's word; a held controller's voter makes no new grant.
	{"hold_released_by_any_boot", []string{"TestAHoldEndsOnlyOnTheMarkingBootsWord", "TestTheFsyncWindowKeepsRulesIAndII"}}, // since 3c-3b a restart keeps the mark: the sim pin is the fsync window
	{"held_voter_grants", []string{"TestAHeldControllersVoterGrantsTheOtherNoNewLease", "TestTheFsyncWindowKeepsRulesIAndII"}},
	{"dup_mark_from_relay", []string{"TestARelayedOldBootNeverMarks"}},
	{"marked_not_told", []string{"TestAMarkedNameGetsNoNewGrantUntilCleared", "TestTheFsyncWindowKeepsRulesIAndII"}},
	{"marked_still_campaigns", []string{"TestAMarkedCandidateStopsCampaigning", "TestTheFsyncWindowKeepsRulesIAndII"}},
	{"discount_reterms", []string{"TestADiscountedGrantDoesNotReterm", "TestCloneSeeds"}},
	{"clear_keeps_mark", []string{"TestClearingAMarkGrantsTheNameAgain"}},
	{"dup_alarm_unconfirmed", []string{"TestVoterRefusesANameUnderAnotherBootNonce", "TestADuplicatedNameGetsNoNewGrant"}},
	// The relay refuses a takeover's first campaign at the witness at
	// times, before its own voter's grant is back: the retry must come
	// half a look on, not a lease.
	{"retry_after_refusal_only", []string{"TestCandidateRetriesWhenItsOwnGrantComesBackLast", "TestStaleRelayEndsAndTheStandbyLeads"}},
	{"reply_relay_dropped", []string{"TestSpoofedCloneOfTheLeaderWithTheABLinkDown"}},
	// #1029 3c-2: a copy that both other voters hold another boot of its
	// name for demotes itself to a spare, its flag durable first, then
	// silent. The sim gates (quorum/sim/spare_test.go) are where a rule
	// alone stands between a real controller and a false spare.
	{"held_by_stale", []string{"TestAStoppedBootIsNoIncumbent"}}, // at the voter only: since the #1133 review a restarted original is told nothing, stale or not
	{"held_by_last_heard", []string{"TestAVoterNamesTheFirstHeardBootTheIncumbent", "TestACloneBesideASettledPairBecomesASpare"}},
	// #1133 review: a refusal names the incumbent as of the frame it
	// answers (a lossy original), and nobody to a boot first heard after
	// the mark (an original restarted beside a running copy).
	{"held_by_before_frame", []string{"TestARefusalCountsTheFrameItAnswers", "TestALossyOriginalNeverGoesSpare"}},
	{"held_by_names_to_unknown", []string{"TestAStoppedBootIsNoIncumbent", "TestAnOriginalRestartingAfterTheMarkStaysAMember"}},
	{"spare_flag_kept_after_failure", []string{"TestAFailedSpareWriteTakesTheFlagBack"}},
	{"peer_dial_lingers", []string{"TestPeerConnectionsCloseWithAReset"}},
	{"spare_one_voter", []string{"TestACopyDemotesOnlyOnBothOtherVotersWordForALease", "TestLateFramesOfARestartMakeNoSpare"}},
	{"spare_own_voter_counts", []string{"TestOnlyTheOtherVotersWordsToThisBootCount"}},
	{"spare_no_span", []string{"TestACopyDemotesOnlyOnBothOtherVotersWordForALease"}},
	{"spare_while_leading", []string{"TestALeaseHolderIsNeverDemoted"}},
	{"state_always_v6", []string{"TestTheSpareFlagIsDurableAndOnlyASpareIsFormat6"}},
	{"spare_flag_after_silence", []string{"TestASpareGoesSilentOnlyOnceItsFlagIsWritten"}},
	{"spare_answers_votes", []string{"TestASpareStartsSilentWhateverTheBox"}},
	{"spare_still_looks", []string{"TestASpareSendsNothing"}},
	{"spare_forgotten_on_box_change", []string{"TestASpareStartsSilentWhateverTheBox"}},
	{"newer_state_moved_aside", []string{"TestANewerStateFileIsRefusedAndLeftAlone"}},
	{"spare_log_runs", []string{"TestASparesPeerListenerAnswersNobody"}},
	// #1029 3c-3a: the operator's clear-duplicate command.
	{"clear_per_member", []string{"TestCopyLiveTakesTheMembersTogether", "TestAClearWhileTheCopyRunsBehindASplitIsRefused"}},
	{"clear_partial_check", []string{"TestClearDuplicateAllNeedsEveryMember"}},
	// #1135 review.
	{"clear_forgets_boots", []string{"TestACopyUnheardAtTheClearIsMarkedWhenItReturns"}},
	{"clear_unsettled", []string{"TestAJustStartedMemberAnswersNoClear"}},
	{"clear_ignores_keep", []string{"TestAClearRefusesABootOtherThanTheOneItKeeps", "TestADupClearReChecksItsOwnVoter"}},
	{"clear_done_ignores_clear", []string{"TestAClearMissedAtAMemberIsNotDoneAndRunsAgain"}},
	{"clear_rerun_refused", []string{"TestAClearMissedAtAMemberIsNotDoneAndRunsAgain"}},
	{"recheck_marks_only", []string{"TestTheRecheckReportsACopyHeardAfterTheClear"}},
	// naut-lead's ruling on the #1135 review: retire the union, keep one.
	{"clear_retires_own_only", []string{"TestACopyKnownToOneMemberIsRetiredAtAll"}},
	{"clear_retires_marked_only", []string{"TestACopyKnownToOneMemberIsRetiredAtAll"}},
	{"clear_without_keep", []string{"TestTheCheckPhaseKeepsOneBootAndRetiresTheRest"}},
	{"cluster_ops_proxied", []string{"TestAStandbyWithNoLeaderAnswersClearDuplicate"}},
	{"redundancy_same_origin", []string{"TestEveryWritePathJournalsExactlyOneEntry"}},
	// #1029 3c-3b: durable marks.
	{"marks_volatile", []string{"TestAMarkSurvivesTheVotersRestart", "TestRetiredBootsSurviveTheVotersRestart", "TestRestartedVotersKeepTheMarkAndTheIncumbent"}},
	{"mark_not_restored", []string{"TestAMarkSurvivesTheVotersRestart"}},
	{"restored_boot_fresh", []string{"TestARestoredIncumbentIsNamedOnceHeardAgain"}},
	{"retired_not_restored", []string{"TestRetiredBootsSurviveTheVotersRestart"}},
	{"mark_write_on_grant_path", []string{"TestAFailingMarkWriteHoldsNoRenewal"}},
	{"retired_reply_counted", []string{"TestAReplyFromARetiredBootCountsNothingAndMarks", "TestACopyPausedOrHungAtTheClearIsMarkedWhenItReturns"}},
	{"release_ignores_gen", []string{"TestAHoldAVoterForgotEndsOnTheClear"}},
	{"clear_gen_not_bumped", []string{"TestAHoldEndsOnlyOnALaterClear"}},
	{"clear_gen_ignores_holds", []string{"TestAClearFreesAHoldWhenAMemberIsRestoredFromABackup"}},
	// #1144 review.
	{"clear_ignores_held", []string{"TestAClearFreesAHoldNoMemberMarksAnyMore"}},
	{"retired_reply_unmatched", []string{"TestALateReplyFromARetiredBootMarksNothing"}},
	{"hold_gen_reset_on_boot", []string{"TestAHoldIsSetUnderTheHighestGenerationSeen"}},
	{"spare_failure_unwritten", []string{"TestAFailedSpareWriteIsWrittenOver"}},
	// #1144 re-check.
	{"hold_gen_ignores_seen", []string{"TestAHoldSetBetweenTheCheckAndTheClearEndsOnTheClear"}},
	{"done_ignores_held", []string{"TestAClearIsNotDoneWhileTheControllerIsHeld"}},
	// #1029 3c-3c: commissioning and the spare's way back.
	{"commission_unchecked", []string{"TestCommissionRefusesAPairThatWasCommissioned"}},
	{"recommission_local_floor_only", []string{"TestTheFloorsEvidenceIsEveryMembersTerms"}},
	{"box_replaced_unchecked", []string{"TestABoxChangedControllerNeedsBoxReplaced"}},
	{"from_spare_not_spare", []string{"TestFromSpareRunsOnlyOnASpare"}},
	{"from_spare_own_view", []string{"TestFromSpareRefusesWhileEitherVoterHearsTheName"}},
	{"from_spare_one_voter", []string{"TestFromSpareRefusesWhileEitherVoterHearsTheName"}},
	{"from_spare_write_after_move", []string{"TestNoWriteAfterTheMoveAside"}},
	{"uncommissioned_looks_unpaced", []string{"TestANewPairWaitingForCommissionIsQuiet"}},
	// #1157 review.
	{"from_spare_peer_unchecked", []string{"TestFromSpareNeedsACommissionedPeer"}},
	{"from_spare_clears_past_refusal", []string{"TestAFromSpareClearThatMissesAVoterMovesNothing"}},
	{"from_spare_moves_after_partial", []string{"TestAFromSpareClearThatMissesAVoterMovesNothing"}},
	// #1029 3c-3c-1b: the adoption-generation fence on --from-spare.
	{"fence_requests_off", []string{"TestAFencedRequestIsRefusedAndIgnored", "TestFromSpareNeverMakesTwoLeaders"}},
	{"fence_replies_off", []string{"TestFromSpareNeverMakesTwoLeaders"}},
	{"fence_witness_unset", []string{"TestFromSpareNeverMakesTwoLeaders"}},
	{"fence_peer_own_gen_only", []string{"TestTheFenceCoversAPeersProposalFarAboveItsGeneration"}},
	{"fence_adopt_off", []string{"TestAWitnessReplyThenTheOldBoxsStateGivesNoProposal"}},
	{"fence_volatile", []string{"TestAFenceIsDurableAndOnlyAFencedFileIsFormat8", "TestFromSpareNeverMakesTwoLeaders"}},
	{"grant_keeps_record", []string{"TestACampaignGrantRecordsItsEpoch", "TestTargetedHotAfterAKill"}},
	{"mark_from_refused_renewal", []string{"TestAMarkComesOnlyFromAGrantedRenewal"}},
	{"reply_mark_after_grant", []string{"TestAMarkComesOnlyFromAGrantedRenewal"}},
	{"mark_term_compared", []string{"TestAMarkComesOnlyFromAGrantedRenewal"}},
	{"mark_from_losing_round", []string{"TestHotCheckReadsTheWinningRoundOnly"}},
	{"unknown_counts_toward_majority", []string{"TestHotVerdict"}},
	{"own_record_ignored", []string{"TestHotVerdict"}},
	{"mark_tie_current_wins", []string{"TestHotVerdict"}},
	{"later_leader_ignored", []string{"TestHotVerdict", "TestTargetedLaterLeadershipGoesCold"}},
	{"records_from_floor_unset", []string{"TestRecordsCoverOnlyWhatTheStateKnows"}},
	{"fence_unnoted", []string{"TestAFencedControllerSaysSoAndCampaignsNoMore", "TestFromSpareNeverMakesTwoLeaders"}},
	{"fenced_still_campaigns", []string{"TestAFencedControllerSaysSoAndCampaignsNoMore", "TestFromSpareNeverMakesTwoLeaders"}},
	{"fence_hold_ignores_witness", []string{"TestAFencedControllerSaysSoAndCampaignsNoMore", "TestFromSpareNeverMakesTwoLeaders"}},
	{"propose_any_echo", []string{"TestAProposalIsTakenOnlyByTheBootItAddresses"}},
	{"propose_resent_at_look", []string{"TestAProposalIsTakenOnlyByTheBootItAddresses", "TestQuietPeriodHoldsOffTheNewWitness"}},
	{"recommission_under_fence", []string{"TestARecommissionProposesAboveTheFence"}},
	// #1164 review: the fence survives a member's file loss, and covers an
	// old disk that died re-adopting.
	{"fence_plus_one", []string{"TestTheFenceIsAboveAnyProposalAnOldDiskHolds", "TestFromSpareNeverMakesTwoLeaders"}},
	{"fence_ignores_previous", []string{"TestASecondFromSpareFencesAboveTheFirst"}},
	{"fenced_propose_answered", []string{"TestTheFenceIsAboveAnyProposalAnOldDiskHolds"}},
	{"fence_unproposed", []string{"TestTheFenceIsAboveAnyProposalAnOldDiskHolds", "TestFromSpareNeverMakesTwoLeaders"}},
	{"fence_not_learned", []string{"TestOnlyAVoterThatLostItsStateLearnsFences", "TestAWitnessReplyThenTheOldBoxsStateGivesNoProposal", "TestFromSpareNeverMakesTwoLeaders"}},
	{"fence_any_name", []string{"TestAFenceNamesOnlyAPairController"}},
	{"dup_answered_before_write", []string{"TestAFromSpareWhoseWitnessCantWriteIsIncomplete"}},
	{"dup_wait_lease_scaled", []string{"TestAClearWaitsOutASlowDisk", "TestDupWriteWaitHasAFloor"}},
	// #1164 re-check.
	{"fence_hides_lookalike", []string{"TestAFencedVoterStillHearsAnUncommissionedLookalike", "TestFromSpareNeverMakesTwoLeaders"}},
	{"fence_learns_any_name", []string{"TestAFenceIsLearntOnlyOnAPairNameAndNeverCarriedToAController"}},
	{"fence_takes_carried", []string{"TestAFenceIsLearntOnlyOnAPairNameAndNeverCarriedToAController"}},
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
	{"yield_to_marked_peer", []string{"TestLoopbackNoYieldToAPeerItsVoterMarked"}},
	{"may_ignores_mark", []string{"TestLoopbackAMarkedControllerSaysItMayNotCampaign"}},
	{"mark_alarm_from_cause", []string{"TestTheConfirmedAlarmSurvivesARelayCountInTheSameHandle"}},
	{"yield_until_due", []string{"TestYieldHoldsUntilTheLeaseIsSeenOver"}},
	// In the log (oplog).
	{"term_not_persisted", []string{"TestLostLogKeepsItsTerm"}},
	{"unnamed_leader_admitted", []string{"TestMarkedReplicaRefusesAnUnnamedLeader"}},
	{"vouch_unchecked", []string{"TestVouchRefuses"}},
	{"vouch_alone_kept", []string{"TestVouchedCatchUpAloneIsOnce", "TestPairCommissioningVouchSyncsTheOtherCopies"}},
	{"mark_after_log", []string{"TestMarkIsWrittenBeforeTheLog"}},
	// #1053 review.
	{"vouch_alone_partial", []string{"TestAVouchWaitsForAnUnacceptedSilentCopyThoughAnotherAnswers"}}, // TestDoubleVouchWithASilentWitnessKeepsTheWrite: (i) refuses it first
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
	// #1029 3c-3c-2a: accepting a loss, and the log history.
	{"accept_loss_blanket", []string{"TestTheCatchUpOverridesOnlyWhatWasAccepted"}},
	{"accept_kept_after_sync", []string{"TestTheAcceptanceIsSpent", "TestACopyAcceptedAsSilentThatCountsIsTaken"}},
	{"lost_log_vouch_unchecked", []string{"TestAcceptLostLogTakesTheWitnessesEntries"}},
	{"synced_alone_without_accept", []string{"TestAcceptLossFreesASyncedLeaderAlone"}},
	{"history_not_bumped", []string{"TestAcceptedSilentBoxStartsANewHistory", "TestTheNewHistoryIsDurableBeforeTheCatchUpEnds"}},
	{"history_after_write", []string{"TestTheNewHistoryIsDurableBeforeTheCatchUpEnds"}},
	{"history_lower_counted", []string{"TestALowerHistoryCopyDoesNotCount"}},
	{"history_fork_counted", []string{"TestAForkFailsClosedUntilAPersonChooses"}},
	{"fork_resolved_without_bump", []string{"TestAForkFailsClosedUntilAPersonChooses"}},
	{"superseded_not_reset", []string{"TestAcceptedSilentBoxStartsANewHistory", "TestOneSupersededLogIsKept"}},
	{"superseded_takes", []string{"TestALeaderThatHearsANewerHistoryCommitsNothing"}},
	{"supersede_not_durable", []string{"TestAcceptedSilentBoxStartsANewHistory"}},
	{"history_moved_commits", []string{"TestALeaderThatHearsANewerHistoryCommitsNothing"}},
	{"lower_history_peer_synced", []string{"TestNoDeferOrYieldToALowerOrForkedHistory"}},
	{"hello_history_ignored", []string{"TestAHigherHistoryInTheHelloIsLearnt"}},
	{"restart_while_leading", []string{"TestASupersededControllerRestartsItselfOnlyUnderTheRule"}},
	{"restart_peer_unchecked", []string{"TestASupersededControllerRestartsItselfOnlyUnderTheRule"}},
	{"restart_unsupervised", []string{"TestASupersededControllerRestartsItselfOnlyUnderTheRule"}},
	{"restart_loops", []string{"TestASupersededControllerRestartsItselfOnlyUnderTheRule"}},
	{"witness_supersede_waits", []string{"TestPairAnAcceptedSilentBoxComesBackSuperseded"}},
	{"retained_stale_ignored", []string{"TestASupersededStandbysTakeoverRestoresNoneOfItsRetainedSubstitutes"}},
	{"stale_retained_kept", []string{"TestAStartAfterASupersedeDropsTheRetainedSubstitutes"}},
	{"override_all_silent", []string{"TestAnOverrideWithEveryOtherCopySilentIsRefused"}},
	// #1177 review.
	{"stale_under_lock", []string{"TestASupersededCheckInTheAlarmRestoreNeverWaitsOnACommit", "TestAnUnsyncedTakeoverNeverWaitsOnTheCatchUpsCommit"}},
	{"vouch_again_noop", []string{"TestVouchAgainAfterARefusedCatchUpAcceptsTheNewPicture"}},
	{"noop_digest_unchecked", []string{"TestANoOpVouchRefusesADigestOfAnotherPicture"}},
	{"vouch_keeps_term_synced", []string{"TestAForkChosenOnTheSyncedLeaderIsResolved"}},
	{"stale_retained_alarms_kept", []string{"TestDropRetainedIntent"}},
	{"takeover_keeps_first_scan", []string{"TestFirstScanOnEveryTakeover"}},
	// #710 S1a: the state image and the standby's shadow.
	{"image_copy_in_scan_encodes", []string{"TestImageCopyAddsNoAllocationToTheScan"}},
	{"image_crc_unchecked", []string{"TestImageDecodeRefusesDamage"}},
	{"image_seq_from_scans", []string{"TestImageAfterAnOnlineEditStillApplies"}},
	{"shadow_older_image_applied", []string{"TestShadowRefusesAnOlderImage"}},
	{"shadow_layout_unchecked", []string{"TestShadowRefusesAnotherLayout"}},
	{"shadow_stale_frame_kept", []string{"TestShadowRefusesAnotherLayout"}},
	{"image_size_unbounded", []string{"TestImageSizeLimits"}},
	{"shadow_kept_after_leading", []string{"TestShadowRefusesAnOlderImage"}},
	{"stream_carries_log_intent", []string{"TestCarry"}},
	// #710 S1b: the standby's term bound on images.
	{"asked_unrecorded", []string{"TestImageTermBoundHoldsThroughAReterm"}},
	{"stream_leader_unseen", []string{"TestImageTermBoundAcrossADeposedLeader"}},
	{"stream_floor_unchecked", []string{"TestImageTermBoundAcrossADeposedLeader", "TestImageTermBoundAfterALateRenewal"}},
	{"stream_term_unbounded", []string{"TestImageTermBoundAcrossADeposedLeader"}},
	{"stream_takeover_no_flush", []string{"TestTakeoverFlushesTheNewestImageBeforeItLoads"}},
	{"stream_leader_carries", []string{"TestCarryRetainedTakesOnlyNewerImages"}},
	{"stream_old_image_carried", []string{"TestCarryRetainedTakesOnlyNewerImages"}},
	{"stream_leader_saves_unmarked", []string{"TestCarryRetainedTakesOnlyNewerImages"}},
	{"stream_stale_capture_sent", []string{"TestATakeoverDropsAFrameCopyMadeBeforeIt"}},
	{"stream_leader_follows", []string{"TestPairRetainedStateSurvivesTakeovers"}},
	{"stream_persist_off", []string{"TestPairRetainedStateSurvivesTakeovers"}},
	{"stream_old_term_applied", []string{"TestPairRetainedStateSurvivesTakeovers"}},
	{"stream_budget_drops", []string{"TestPairStreamBudgetStretchesTheInterval"}},
	{"stream_flush_after_io_read", []string{"TestTakeoverFlushesTheNewestImageBeforeItLoads"}},
	{"stream_flush_unbounded", []string{"TestTakeoverFlushIsBounded"}},
	{"stream_flush_term_unkeyed", []string{"TestATakeoverFlushesAgainForANewTerm"}},
	{"stream_failed_load_streams", []string{"TestRetainLoadFailedOnlyForAnUnreadableStore", "TestPairALeaderWithAnUnreadableStoreStreamsNothing"}},
	{"stream_store_bad_forgotten", []string{"TestPairALeaderWithAnUnreadableStoreStreamsNothing"}},
	{"stream_sends_unled", []string{"TestPairStreamEndsWhenTheLeaderStepsDown"}},
	{"stream_flush_no_persist", []string{"TestPairTakeoverFlushesAnImageNotYetPersisted"}},
	{"stream_flush_keeps_follower", []string{"TestPairTakeoverFlushesAnImageNotYetPersisted"}},
	{"stream_flush_keeps_shadow", []string{"TestPairTakeoverFlushesAnImageNotYetPersisted"}},
	{"stream_stale_marked_persisted", []string{"TestPairStandbyTakesNothingBelowItsFile"}},
	{"stream_file_floor_unseeded", []string{"TestPairStandbyTakesNothingBelowItsFile"}},
	{"stream_persist_error_hidden", []string{"TestPairStandbySaysItsFileTakesNoImage"}},
	{"stream_outlives_close", []string{"TestPairAClosingLeaderEndsItsStreamsAtOnce"}},
	// #710 1 s default lease: a voter's lease refusal is loud, named and cleared.
	{"lease_mismatch_never_clears", []string{"TestLoopbackWitnessRefusingTheLeaseIsRaisedAndCleared", "TestLoopbackLeaseUpgradeOrderReportsOnceAndEndsClear", "TestPairLeaseRefusalsAreRaisedAndCleared"}},
	{"lease_refusal_one_way", []string{"TestLoopbackLeaseUpgradeOrderReportsOnceAndEndsClear"}},
	{"lease_clear_on_wait", []string{"TestLoopbackWitnessRefusingTheLeaseIsRaisedAndCleared"}},
	{"lease_condition_dropped", []string{"TestPairLeaseRefusalsAreRaisedAndCleared"}},
	{"witness_lease_no_alarm", []string{"TestPairLeaseRefusalsAreRaisedAndCleared"}},
	{"lease_check_unwarned", []string{"TestCheckWarnsOfALeaseUnderOneSecond"}},
	// #710 S2a: the image carries every tag the field does not provide, and the sources.
	{"image_tags_carry_io", []string{"TestImageCarriesEveryTagTheFieldDoesNotProvide", "TestHotTakeoverGoesOnFromTheImage"}},
	{"image_tags_written_only", []string{"TestImageCarriesEveryTagTheFieldDoesNotProvide"}},
	{"image_tag_forced_value", []string{"TestImageCarriesEveryTagTheFieldDoesNotProvide"}},
	{"image_tags_at_take", []string{"TestImageTagsOfOneInstantWithTheFrame"}},
	{"copy_first_scan_only", []string{"TestImageOfTasksOfTwoRatesIsFromAfterTheSlowestCopy"}},
	{"copy_from_older_round", []string{"TestImageNeverTakesACopyOfAnEarlierRound"}},
	{"image_source_unhashed", []string{"TestImageSourceNamesTheRunningSource"}},
	{"shadow_tags_layout_unchecked", []string{"TestShadowRefusesAnotherTagsLayout"}},
	{"image_tags_retained_twice", []string{"TestImageCarriesEveryTagTheFieldDoesNotProvide"}},
	{"stream_format_ignored", []string{"TestPairStreamSendsEachStandbyAFormatItReads", "TestStreamAskedFormat"}},
	// #710 S2b: the hot takeover.
	{"hot_no_age_check", []string{"TestHotTakeoverColdCases"}},
	{"hot_source_unchecked", []string{"TestHotTakeoverColdCases"}},
	{"hot_partial_tasks", []string{"TestHotTakeoverColdCases"}},
	{"hot_globals_not_carried", []string{"TestHotTakeoverGoesOnFromTheImage", "TestPairHotTakeover"}},
	{"hot_timer_not_rebased", []string{"TestHotTakeoverGoesOnFromTheImage"}},
	{"hot_age_not_elapsed", []string{"TestHotTakeoverGoesOnFromTheImage"}},
	{"hot_late_flush_installs", []string{"TestHotTakeoverNeverInstallsALateFlush"}},
	{"hot_failed_flush_installs", []string{"TestHotTakeoverColdCases"}},
	{"hot_ticket_carried", []string{"TestHotTakeoverGoesOnFromTheImage"}},
	{"retain_memory_only_forgotten", []string{"TestHotTakeoverOverAnUnreadableStore", "TestPairHotTakeoverOverAnUnreadableRetainFile"}},
	{"hot_retain_before_checks", []string{"TestHotTakeoverOverAnUnreadableStoreGoesColdOnAFailedCheck"}},
	{"hot_rebase_per_task", []string{"TestHotTakeoverOfAMultiRateImage"}},
	{"hot_build_unchecked", []string{"TestPairColdTakeoverOnAnotherBuild"}},
	{"hot_voters_unchecked", []string{"TestPairLaterLeadershipGoesCold"}},
	{"ack_old_epoch_counts", []string{"TestAckOfAnotherEpochOrAnUnsentImageRefused"}},
	{"current_time_based", []string{"TestCurrentIsJudgedOnSequences", "TestPairHotTakeoverWithASlowTask"}},
	{"one_mark_enough", []string{"TestHotVerdict"}},
	{"evidence_from_covering_only", []string{"TestHotVerdict"}},
	{"mark_from_older_renewal", []string{"TestAnOlderRenewalNeverReplacesANewerMark"}},
	{"current_without_fresh_send", []string{"TestAStoppedStreamIsNotCurrent"}},
	{"interval_across_reconnect", []string{"TestAStoppedStreamIsNotCurrent"}},
	{"hot_cold_condition_never_clears", []string{"TestPairColdTakeoverOnAnotherBuild"}},
	// #710 S2c-3: the bad-retain hand-back, and naut-lead's S5 ruling on
	// #1345 (a discounted voter's reply never vouches).
	{"discounted_vouches", []string{"TestADiscountedReplyNeverVouches"}},
	{"prior_boot_overwritten", []string{"TestAReplyTellsTheAskerOfItsEarlierBootsOnly"}},
	{"own_record_after_grant", []string{"TestAReplyTellsTheAskerOfItsEarlierBootsOnly"}},
	{"handback_guard_off", []string{"TestHandBackGuard", "TestPairHandBackOnceInACrashLoop"}},
	{"handback_future_held", []string{"TestHandBackGuard"}},
	{"handback_own_record_ignored", []string{"TestHandBackFloor", "TestPairNoHandBackToAFileOlderThanOwnEarlierLeadership"}},
	{"handback_unknown_counts", []string{"TestHandBackFloor"}},
	{"handback_keeps_renewing", []string{"TestLoopbackHandBackGivesTheLeadToThePeer", "TestPairHandBackFallsBackWhenThePeerNeverTakes"}},
	{"handback_guard_volatile", []string{"TestLoopbackHandBackGivesTheLeadToThePeer", "TestPairHandBackOnceInACrashLoop"}},
	{"handback_long_deadline", []string{"TestPairHandBackToASilentPeer"}},
	{"handback_ineligible_peer", []string{"TestPairNoHandBackToAPeerThatCannotLeadOnItsFile"}},
	{"handback_peer_guard_off", []string{"TestPairHandBackOnceInACrashLoop"}},
	{"handback_empty_file_good", []string{"TestPairNoHandBackToAPeerThatCannotLeadOnItsFile"}},
	{"handback_peer_file_unjudged", []string{"TestPairNoHandBackToAFileOlderThanOwnEarlierLeadership"}},
	{"handback_taken_unalarmed", []string{"TestPairHandBackToAGoodPeer"}},
	{"handback_on_readable_store", []string{"TestPairHandBackToAGoodPeer"}},
	{"handback_when_hot", []string{"TestPairHotTakeoverOverAnUnreadableRetainFile"}},
	{"handback_guard_kept_on_failure", []string{"TestLoopbackFailedHandBackArmsNoGuard"}},
	{"handback_store_bad_forgotten", []string{"TestPairHandBackFallsBackWhenThePeerNeverTakes"}},
	{"handback_restart_empty_trusted", []string{"TestPairHandBackOnceInACrashLoop"}},
	{"handback_floor_max_only", []string{"TestHandBackFloor"}},
	// #710 (b1): the nautilus system functions.
	{"hot_first_scan_true", []string{"TestHotTakeoverGoesOnFromTheImage", "TestSystemFunctionsAcrossTakeovers"}},
	{"takeover_unmarked", []string{"TestSystemFunctionsAcrossTakeovers", "TestPairSystemFunctions"}},
	{"takeover_flag_kept", []string{"TestSystemFunctionsAcrossTakeovers"}},
	{"takeover_flag_survives_step_down", []string{"TestATakeoverFlagDoesNotSurviveAStepDown"}},
	{"peer_ready_never_set", []string{"TestPeerReadyAndAnEditAfterATakeover", "TestPairSystemFunctions"}},
	{"every_acquisition_a_takeover", []string{"TestLoopbackAFirstLeaderStartsAStandbyTakesOver", "TestPairSystemFunctions"}},
	{"peer_ready_ignores_forces", []string{"TestPairSystemFunctions"}},
	// #710 (b2): the takeover hold.
	{"hold_never_starts", []string{"TestTakeoverHoldHoldsUntilTheProgramReleases", "TestTakeoverHoldWhenItHolds", "TestPairTakeoverHoldColdAndOperatorRelease"}},
	{"hold_release_tag_kept", []string{"TestTakeoverHoldWhenItHolds"}},
	{"hold_drivers_remember", []string{"TestTakeoverHoldHoldsUntilTheProgramReleases"}},
	{"hold_safe_unsent", []string{"TestTakeoverHoldHoldsUntilTheProgramReleases", "TestTakeoverHoldSafeValueDelivery"}},
	{"hold_not_inherited", []string{"TestTakeoverHoldInheritedWithItsAge", "TestPairTakeoverHoldInheritedAndProgramRelease"}},
	{"hold_age_restarts", []string{"TestTakeoverHoldInheritedWithItsAge", "TestPairTakeoverHoldInheritedAndProgramRelease"}},
	{"hold_no_max", []string{"TestTakeoverHoldStaysHeldAtMax", "TestPairTakeoverHoldColdAndOperatorRelease"}},
	{"hold_release_tag_writable", []string{"TestTakeoverHoldReleaseTagIsTheProgramsOnly"}},
	{"hold_channel_writes", []string{"TestTakeoverHoldRefusesChannelWrites", "TestTakeoverHoldRefusesAWritePostedHeld"}},
	{"hold_not_imaged", []string{"TestTakeoverHoldRidesInTheImage", "TestPairTakeoverHoldInheritedAndProgramRelease"}},
	{"hold_forget_nothing", []string{"TestModbusForgetOutputsDropsAnEarlierLeadership"}},
	{"hold_safe_never_lands", []string{"TestModbusSafeValueIsCommandedAndConfirmed"}},
	{"hold_release_at_max_ignored", []string{"TestTakeoverHoldReleasesAtMaxWhenOptedIn"}},
	{"hold_replace_unsafe", []string{"TestTakeoverHoldSafeResendAndReplace"}},
	{"hold_channel_held_at_post", []string{"TestTakeoverHoldRefusesAWritePostedHeld"}},
	{"hold_safe_wait_restarts", []string{"TestTakeoverHoldSafeAlarmDespiteRetries"}},
	{"resign_ignores_epoch", []string{"TestResignMatchesTheLeadershipItEnds"}},
	{"resign_keeps_renewal_seen", []string{"TestResignHandsOverWithoutWaitingTheLease", "TestResignMatchesTheLeadershipItEnds"}},
	{"resign_keeps_heard", []string{"TestResignClearsAndIgnoresRelaysToTheResignedBoot"}},
	{"resign_renewal_granted", []string{"TestResignHandsOverWithoutWaitingTheLease", "TestResignMatchesTheLeadershipItEnds"}},
	{"resign_relay_heard", []string{"TestResignClearsAndIgnoresRelaysToTheResignedBoot"}},
	{"resign_no_hold", []string{"TestAResignedLeaderHoldsOffThenLeadsAgain", "TestSimResignHandsOverHotWithinAQuarterLease"}},
	{"resign_own_keeps_heard", []string{"TestResignClearsRelaysAtTheLeadersOwnVoter"}},
	{"resign_ignores_seq", []string{"TestAResignOlderThanARenewalHeardIsRefused"}},
	{"resign_keeps_rounds", []string{"TestALateRenewalGrantDoesNotReLeadAfterTheHoldEnds", "TestSimAnEarlyHoldEndIsSafe"}},
	{"resign_quiet_outlives_grant", []string{"TestTheRelayQuietEndsWhenTheBootIsGrantedAgain"}},
	{"resign_dup_acts", []string{"TestSimAnOldResignCopyDoesNotWakeTheResigner"}},
	{"resign_mark_dropped", []string{"TestResignMatchesTheLeadershipItEnds"}},
	{"resign_after_promise", []string{"TestAResignAfterThePromiseRanOutIsRefused"}},
	{"resign_publish_after_send", []string{"TestLoopbackAResignHandsTheLeadOverAtOnce"}},
	{"resign_record_only", []string{"TestResignHandsOverWithoutWaitingTheLease"}},
	{"resign_peer_first", []string{"TestLoopbackAResignHandsTheLeadOverAtOnce"}},
	{"resign_waits_for_look", []string{"TestLoopbackAResignHandsTheLeadOverAtOnce"}},
	{"resign_keeps_own_hold", []string{"TestLoopbackAResignHandsTheLeadOverAtOnce"}},
	// #710 d1b: the outputs' drain before a RESIGN.
	{"resign_before_drain", []string{"TestLoopbackAResignHandsTheLeadOverAtOnce"}},
	{"resign_despite_drain_error", []string{"TestLoopbackAnUnfinishedDrainSendsNoResign"}},
	{"resign_frees_own_before_drain", []string{"TestTheOwnVoterKeepsItsPromiseUntilTheDrain"}},
	{"resign_drained_unchecked", []string{"TestALateDrainFreesNothing"}},
	{"resign_despite_late_drain", []string{"TestLoopbackALateDrainSendsNoResign"}},
	{"handback_keeps_resign_hold", []string{"TestPairHandBackEndsTheResignersHold"}},
	{"hold_ends_in_drain", []string{"TestAResignHoldDoesNotEndBeforeTheDrain"}},
	{"handback_any_epoch", []string{"TestAHandBackOfAnOldEpochEndsNoHold", "TestPairAStaleHandBackKeepsTheResignHold"}},
	{"enter_after_read", []string{"TestDrainWaitsForASendThatReadTheGateOpenJustBeforeItClosed"}},
	{"drain_no_wait", []string{"TestDrainWaitsForASendThatReadTheGateOpenJustBeforeItClosed", "TestDrainSettlesAfterTheLastSend", "TestDrainUnprovenWhenASendNeverEnds"}},
	{"drain_no_reset", []string{"TestDrainResetsAHungSend", "TestDuringADrainLateSendsAreCutShort", "TestOptoMMPWriteIsCountedAndCutShortByTheDrain", "TestADrainCutsAHungCommandShort"}},
	{"drain_no_settle", []string{"TestDrainSettlesAfterTheLastSend"}},
	{"host_enter_after_gate", []string{"TestHostCommandReadingTheGateOpenHoldsTheDrain"}},
	// #798: a Sparkplug edge node under redundancy.
	{"spark_ungated", []string{"TestSparkplugSessionFollowsTheLeadership", "TestPairSparkplugOneSessionThroughSwitchoverFailoverAndBack"}},
	{"spark_no_death_on_stepdown", []string{"TestPairSparkplugOneSessionThroughSwitchoverFailoverAndBack"}},
	{"spark_bdseq_clash", []string{"TestANewSessionsBdSeqSkipsThePeersLast", "TestPairSparkplugOneSessionThroughSwitchoverFailoverAndBack"}},
	{"spark_publish_after_gate", []string{"TestAGatedNodeNeitherBirthsNorPublishesNorBuffers", "TestAGatedNodeRefusesCommandWrites"}},
	{"spark_death_watch_off", []string{"TestALateDeathOfAnotherSessionBirthsAgain", "TestPairSparkplugOneSessionThroughSwitchoverFailoverAndBack"}},
	{"spark_send_uncounted", []string{"TestFencedPublishesAreCountedForTheDrain"}},
	{"spark_gate_unlatched", []string{"TestSparkplugSessionGateLatchesAndTheSessionRestarts"}},
	{"spark_step_unfenced", []string{"TestSparkplugStepDownFencesASessionStillStarting"}},
	// #710 d1c: the planned switchover.
	{"switch_writes_allowed", []string{"TestSwitchingStopsTheScansAndRefusesWrites"}},
	{"switch_scans_on", []string{"TestSwitchingStopsTheScansAndRefusesWrites"}},
	{"switch_no_wait", []string{"TestSwitchingWaitsForTheScanUnderWay"}},
	{"switch_ignores_forces", []string{"TestSwitchingRefusedWithForcesOrWhenNotLeading"}},
	{"switch_ignores_hold", []string{"TestSwitchingRefusedWhileAHoldHolds"}},
	{"abort_keeps_switching", []string{"TestSwitchingStopsTheScansAndRefusesWrites", "TestPairSwitchoverAbortsWithoutTheFinalAck"}},
	{"resume_ends_switching", []string{"TestSwitchingSurvivesAGateBlipAndEndsAtTheNextLeadership"}},
	{"takeover_keeps_switching", []string{"TestSwitchingSurvivesAGateBlipAndEndsAtTheNextLeadership"}},
	{"final_keeps_round", []string{"TestTheFinalImageClosesTheOpenRound"}},
	{"isdu_write_unfenced", []string{"TestCallFencedGoesOnlyThroughAnOpenGate"}},
	{"final_image_unsent", []string{"TestPairSwitchoverIsHot"}},
	{"capture_ignores_switching", []string{"TestPairSwitchoverAbortsWithoutTheFinalAck"}},
	{"final_ack_any_seq", []string{"TestWaitAckTakesOnlyTheFinalImage"}},
	{"switch_unready_target", []string{"TestPairSwitchoverRefusals"}},
	{"failback_unknown_as_known", []string{"TestAnUnreadableStoreHandsAnUnknownFailbackState", "TestATakeoverAfterTheStoreFailedHandsAnUnknownFailbackState"}},
	{"failback_not_carried", []string{"TestCarry"}},
	{"switch_ignores_io_reach", []string{"TestPairSwitchoverRefusedForAStandbyThatDoesNotReach"}},
	{"failback_ready_never_resets", []string{"TestFailbackWaitsForAfterWithoutABreak"}},
	{"failback_ignores_suspend", []string{"TestFailbackOnlyFromTheOtherLeaderUnsuspended"}},
	{"failback_never_stops", []string{"TestFailbackPingPongStops"}},
	{"failback_count_never_resets", []string{"TestFailbackPingPongStops"}},
	{"failback_manual_not_suspended", []string{"TestFailbackManualSwitchoverAwaySuspends"}},
	{"failback_failover_not_counted", []string{"TestFailbackCountsAFailbackThatEndedAsAFailover"}},
	{"failback_undo_overwrites", []string{"TestFailbackUndoKeepsAnOperatorsSuspend"}},
	{"failback_abort_keeps_suspend", []string{"TestPairFailbackIsHotAndAManualSwitchoverSuspends"}},
	{"failback_suspend_not_saved", []string{"TestPairFailbackSuspendIsSavedBeforeTheAnswer"}},
	{"failback_ignores_prefer_mismatch", []string{"TestPairFailbackRefusesAStandbyThatPrefersOtherwise"}},
	{"witness_covers_below_floor", []string{"TestHotVerdictWitnessBelowItsFloor", "TestHandBackFloorAfterAWitnessRebuild"}},
	{"witness_reason_any", []string{"TestHotVerdictWitnessBelowItsFloor"}},
	{"witness_floor_unwired", []string{"TestAdoptionGivesTheHotCheckTheFloor", "TestHandBackFloorAfterAWitnessRebuild"}},
	{"switch_ignores_standby_io", []string{"TestPairSwitchoverRefusedForAStandbyWithBadIO"}},
	{"abort_keeps_final", []string{"TestPairSwitchoverAbortsWithoutTheFinalAck"}},
	{"final_waits_interval", []string{"TestAFinalImageWaitingIsSentAtOnce"}},
	// #710, the lost switchover campaign: a pending campaign is asked
	// again, same term and id. With every write past lease/8 the pair
	// never elects under no_reask or reask_new_term: the rig's slow-disk
	// test shows it through naut run, and TestLoopbackEveryWriteSlowStill
	// ElectsAndSwitches too, but the loopback tests skip under a core
	// mutation (QUORUM_LOOPBACK=1 runs them).
	{"no_reask", []string{"TestCandidateBackOffWhenUnheard", "TestTargetedDeafCandidateReasksAndStillLoses", "TestPairSlowDisksStillSwitchAndFailOver"}},
	{"reask_new_term", []string{"TestCandidateBackOffWhenUnheard", "TestTargetedDeafCandidateReasksAndStillLoses", "TestPairSlowDisksStillSwitchAndFailOver"}},
	{"reask_lease_from_last_ask", []string{"TestCandidateLeaseFromTheAskItsGrantsAnswer"}},
	{"reask_lease_from_first_send", []string{"TestCandidateLeaseFromTheAskItsGrantsAnswer", "TestASingleRestartRaisesNoDuplicate"}},
	{"backoff_from_first_send", []string{"TestCandidateBackOffWhenUnheard", "TestTargetedDeafCandidateReasksAndStillLoses"}},
	{"reask_declined_stays", []string{"TestCandidateNextLookNeverPastWhenAReaskIsDeclined"}},
	{"grant_sent_last_reply", []string{"TestCandidateLeaseFromTheAskItsGrantsAnswer"}},
	{"flush_next_tick", []string{"TestATakeoverTakesAQuickFlushInItsFirstTick"}},
	{"catchup_next_tick", []string{"TestATakeoverTakesAQuickCatchUpInItsFirstTick"}},
	{"edge_wait_blocks_others", []string{"TestOtherTasksDoNotWaitOnATakeoverEdge"}},
	{"leading_term_locks", []string{"TestAFlushThatCarriesAndAsksTheLeadershipIsNotHeldByTheWait"}},
	{"wait_keeps_lead_sample", []string{"TestALeaseLostDuringTheWaitTakesNothingOver"}},
	{"beginswitch_reads_snapshot", []string{"TestASwitchoverOfAnOldTermDuringATakeoverIsRefused"}},
	{"carry_snapshot_any_wait", []string{"TestACarryDuringTheCatchUpWaitIsRefusedOnceLeading"}},
	{"save_waits_on_edge", []string{"TestASaveDuringTheFlushWaitDoesNotHoldTheCarry"}},
	{"flush_wait_after_start", []string{"TestASaveAskedAsTheFlushStartsIsNotParked"}},
	{"abort_twice_fails", []string{"TestASecondAbortReportsTheReset"}},
	{"resign_unchecked", []string{"TestPairSwitchoverAbortsWithoutTheFinalAck"}},
	{"final_sent_once_per_connection", []string{"TestPairSwitchoverAbortsWithoutTheFinalAck"}},
	{"ack_other_build", []string{"TestPairColdTakeoverOnAnotherBuild"}},
	{"ack_kept_across_reconnect", []string{"TestPairColdTakeoverOnAnotherBuild"}},
	{"spend_failure_keeps_alone", []string{"TestASpendThatCantBeWrittenIsSpentInMemoryAndRetried"}},
	{"accept_survives_reboot", []string{"TestAVouchDoesNotSurviveARestart"}},
	{"superseded_lead_keeps_substitutes", []string{"TestPairALeaderThatLearnsANewerHistoryRunsNoneOfTheOld"}},
	{"superseded_alarms_kept", []string{"TestPairAnAcceptedSilentBoxComesBackSuperseded", "TestPairALeaderThatLearnsANewerHistoryRunsNoneOfTheOld"}},
	// #715 PR 1: alarm occurrences, the pair's occurrence-bound acks, the
	// alarm contacts' hello part.
	{"contacts_part_users", []string{"TestContactsCheck"}},
	{"occurrence_per_activation", []string{"TestOccurrenceRunsNormalToNormal"}},
	{"ack_any_occurrence", []string{"TestAckNamingAnotherOccurrenceIsRefused", "TestAckNamesItsOccurrence"}},
	{"carry_on_false", []string{"TestRestartFalseEndsTheOccurrence"}},
	{"carry_ack_after_false", []string{"TestRestartGraceCarriesNoAck"}},
	{"carry_unbounded", []string{"TestRestartUnreadableHoldsEnableFalseEnds"}},
	{"carry_through_enable", []string{"TestRestartUnreadableHoldsEnableFalseEnds"}},
	{"carry_wait_reset", []string{"TestRestartContinuesTheOccurrence", "TestRestartResendsTheCurrentLevelOnce"}},
	{"ack_clear_keeps_occurrence", []string{"TestAckClearResidualIsNarrowed"}},
	{"takeover_drops_occurrences", []string{"TestTakeoverContinuesTheViewsOccurrences"}},
	{"ack_entry_unbound", []string{"TestLoggedAckIsBoundToItsOccurrence"}},
	{"apply_ack_any_occurrence", []string{"TestLoggedAckIsBoundToItsOccurrence"}},
	{"carry_end_silent", []string{"TestRestartFalseEndsTheOccurrence", "TestPairFieldDropsAtTheTakeover"}},
	{"adopt_off", []string{"TestCommittedAckAdoptsTheStandingOccurrence", "TestPairUnsyncedTakeoverCarriesNoAcks"}},
	{"adopt_ignores_log", []string{"TestLateAckAdoptsOnlyWhatTheLogLeavesStanding"}},
	{"carry_unclamped", []string{"TestCarriedTimesNeverAheadOfTheClock", "TestRestartWithTheClockBackStillPages"}},
	{"adopt_after_suppress", []string{"TestSuppressedSinceTheRestoreIsNotAdopted"}},
	{"carry_ack_resaved_after_false", []string{"TestSecondRestartInsideTheGraceKeepsTheAckDropped"}},
	// #1249 slice 1a: live chattering and flood detection.
	{"chatter_never", []string{"TestChatterStartsAndEndsByTime", "TestLiveChatterMatchesTheReport"}},
	{"chatter_no_merge", []string{"TestChatterOverlappingWindowsAreOneEpisode", "TestLiveChatterMatchesTheReport"}},
	{"flood_no_hysteresis", []string{"TestFloodHysteresis"}},
	{"grace_counts", []string{"TestReactivationsAfterARebaselineDoNotCount"}},
	{"restart_counts", []string{"TestReactivationsAfterARebaselineDoNotCount"}},
	{"first_out_repeats", []string{"TestJournalHidesDetectionUnlessAsked"}},
	{"clock_step_unhandled", []string{"TestDetectionSurvivesAClockStepBack", "TestStartGraceSurvivesAClockStepBack"}},
	{"standing_sticks", []string{"TestReactivationsAfterARebaselineDoNotCount"}},
	{"unsuppress_always_rebaselines", []string{"TestReturnFromSuppressionCountsWhatIsNew"}},
	{"standing_unmarked", []string{"TestReactivationsAfterARebaselineDoNotCount", "TestReportsSkipStandingActivations"}},
	{"seen_when_unmarked", []string{"TestActivationOnAReturnSaysWhatCameBack"}},
	// #1249 slice 3: chatter pages once per level, floods group pages.
	{"chatter_pages_per_bounce", []string{"TestChatterPagesOncePerLevel", "TestChatterAckBetweenBounces"}},
	{"chatter_pin_latched", []string{"TestChatterLeavesTheLatchedWaitAlone"}},
	{"chatter_clock_per_wait", []string{"TestChatterThroughNormalSendsOnce", "TestChatterAckBetweenBounces"}},
	{"regroup_unbounded", []string{"TestFloodGroupsPages"}},
	{"group_covers_on_failure", []string{"TestFloodFailedGroupFallsBackToIndividual"}},
	{"group_individual_ignored", []string{"TestFloodGroupsPages", "TestFloodAndChatter", "TestFloodIndividualAdds"}},
	{"notice_carries_ack", []string{"TestChatterPagesOncePerLevel"}},
	{"takeover_resend_individual", []string{"TestFloodTakeoverResendsGroupedOnce", "TestFloodCarriedOverARestartResendsGrouped"}},
	{"chatter_policy_at_start", []string{"TestChatterFollowsTheWaitsPolicy"}},
	{"chatter_joins_late", []string{"TestChatterPagesLevel1EvenWhenLate"}},
	{"chatter_end_burst", []string{"TestChatterEndDoesNotRepage", "TestSecondChatterEndDoesNotRepage"}},
	{"chatter_end_skips_new", []string{"TestActivationRightAfterChatterPages"}},
	{"group_drop_lost", []string{"TestFloodDroppedGroupSendIsNotLost"}},
	{"released_batch_floors", []string{"TestFloodReleasedBatchHoldsNoFloor"}},
	{"safety_case_exact", []string{"TestFloodSafetyClassAnyCase"}},
	{"chatter_repeats_burst", []string{"TestChatterLateSendsEachLevelOnce", "TestChatterPolicyMoveSendsEachLevelOnce"}},
	// #1249 slice 1b: episodes carried across a restart or takeover.
	{"carry_detect_unclamped", []string{"TestCarriedEpisodesEndWithinAWindowOfTheRestore"}},
	{"takeover_drops_episodes", []string{"TestTakeoverContinuesTheViewsEpisodes", "TestPairEpisodesCarryAcrossTheLeadersDeath"}},
	{"episodes_not_synced", []string{"TestLeaderSyncsEpisodes", "TestPairEpisodesCarryAcrossTheLeadersDeath"}},
	{"flood_end_any", []string{"TestDetectEntriesNameWhatTheyEnd"}},
	{"episodes_stamped_now", []string{"TestColdStartTakeoverEndsStaleEpisodes"}},
	{"carried_chatter_splits", []string{"TestCarriedChatterDoesNotSplitWhileTheRingRefills"}},
	{"adopt_under_mu", []string{"TestReconcileDuringACommitWithAnAdoptionPending"}},
	{"engine_event_waits", []string{"TestScanNeverWaitsOnTheAlarmLog"}},
	{"clear_any_later_ack", []string{"TestLateHandOffKeepsALaterAck"}},
	{"clear_ack_taken_after", []string{"TestHandOffNeverClearsAnAckTakenAfterTheEvent"}},
	{"engine_event_blocks", []string{"TestAFullHandOffDropsAndReconcileClears"}},
	{"clear_while_engine_holds", []string{"TestAnEventBetweenTheViewsAckAndTheEnginesLeavesTheAck"}},
	{"clear_without_livemu", []string{"TestJudgementWaitsOutAnInFlightApply"}},
	{"reconcile_without_livemu", []string{"TestReconcileNeverClearsAnAckAppliedBetweenItsReads"}},
	{"ack_clear_any_occurrence", []string{"TestAckClearOfAnotherOccurrenceLeavesTheAck"}},
	{"engine_event_takes_livemu", []string{"TestScanNeverWaitsOnTheAlarmLog"}},
	{"occurrences_not_synced", []string{"TestLeaderSyncsOccurrences"}},
	{"link_replay_applied", []string{"TestAckLinkIsSpentThroughTheLog"}},
	{"link_spent_unchecked", []string{"TestAckLinkIsSpentOnce", "TestAckLinkSurvivesATakeover"}},
	{"link_ignores_standing", []string{"TestAckLinkStopsWithItsPerson"}},
	{"link_sign_in_ignored", []string{"TestAckLinkSignInMode"}},
	{"link_needs_write_rule", []string{"TestAckLinkNeedsNoOtherCredential", "TestInboundNeedsNoOtherCredential"}},
	{"link_not_forwarded", []string{"TestAckLinkIsForwardedByAStandby"}},
	{"leader_headers_unbounded", []string{"TestStandbyForwardToAHungLeaderIs502", "TestLeaderTransportsHaveDeadlines"}},
	{"link_reason_html", []string{"TestAckLinkRefusalReasonIsWords", "TestAckLinkWithNoLeaderSaysSo"}},
	{"link_acks_on_standby", []string{"TestAckLinkNotLeadingNeverAcksItsEngine"}},
	{"link_session_cookie_refused", []string{"TestAckLinkIgnoresASessionFromAnotherOrigin"}},
	{"inbound_unsigned", []string{"TestInboundSignatureIsOverTheConfiguredURL"}},
	{"inbound_tries_unlimited", []string{"TestInboundWrongCodesAreLimited", "TestTriesWindow"}},
	{"inbound_callee_unchecked", []string{"TestInboundVoiceKeypress"}},
	{"inbound_any_key", []string{"TestInboundVoiceKeypress"}},
	{"inbound_status_on_wire", []string{"TestInboundSMSRefusals"}},
	{"inbound_not_forwarded", []string{"TestInboundOnAStandby"}},
	{"voice_link_acks_here", []string{"TestInboundVoiceKeypress"}},
	{"inbound_replies_unlimited", []string{"TestInboundRepliesAreCapped"}},
	{"inbound_no_global_budget", []string{"TestInboundWrongCodesAreLimited"}},
	{"inbound_any_alarm", []string{"TestInboundCodesOnlyForAlarmsThatPageThePerson"}},
	{"inbound_unknown_as_refused", []string{"TestInboundUnknownOutcome"}},
	{"sms_ack_offered_while_suspended", []string{"TestSMSAckSuspended"}},
	{"inbound_stale_code_counted", []string{"TestInboundSMSAcks"}},
	// #1029 3c-3c-2b: D1 (a wiped old box beside its replacement) and a
	// new pair's plain vouch. Under QUORUM_LOOPBACK=1 (a core mutation
	// skips the loopback tests here), TestALostStateRestartsProbeReaches
	// ThePeerUnmarked catches the first as well, and TestAWipedOldBox
	// BesideItsReplacementIsMarkedOnTheWire the second (the lost-state
	// test catches it only sometimes: the #1207 review).
	{"adopt_probe_unseq", []string{"TestAWipedOldBoxBesideItsReplacementIsMarked"}},
	{"adopt_probe_witness_only", []string{"TestAWipedOldBoxBesideItsReplacementIsMarked"}},
	{"peer_takes_beside_live", []string{"TestAWipedOldBoxBesideItsReplacementIsMarked"}},
	{"recommission_beside_live", []string{"TestRecommissionRefusesBesideALiveBootOfItsName", "TestRecommissionRefusesWhileAVoterCantYetSay"}},
	{"live_check_witness_skipped", []string{"TestRecommissionRefusesWhenTheWitnessMissesTheLiveCheck"}},
	{"empty_pair_needs_accept", []string{"TestANewPairsPlainVouchAfterARestartNeedsNoAcceptLoss", "TestPairANewPairsPlainVouchNeedsNoAcceptLoss"}},
	{"empty_pair_unchecked", []string{"TestANewPairsPlainVouchNamesTheConditionThatFails"}},
	{"set_aside_forgotten", []string{"TestANewPairsPlainVouchNamesTheConditionThatFails"}},
	// Peer TLS (#710 PR 1).
	{"peer_tls_short_token", []string{"TestShortTokenRefused", "TestPairShortTokenRefusedWithPeerTLS", "TestWitnessPeerTLSFlag"}},
	{"peer_tls_server_any_client", []string{"TestServerRefusesAClientWithoutACertificate", "TestAnUnsignedCertificateIsRefused", "TestFaultOf"}},
	{"peer_tls_client_any_server", []string{"TestFirstContactPinsBothEnds", "TestNamesMustMatch", "TestLoopbackImpostorAtTheWitnessAddress", "TestFaultOf"}},
	{"peer_tls_default_off", []string{"TestRedundancyWithPeersIsThePair", "TestWitnessServesVotes", "TestPairShortTokenRefusedWithPeerTLS", "TestWitnessPeerTLSFlag", "TestCheckPeerTLSWarnings"}},
	{"peer_tls_log_plain", []string{"TestPairWitnessLogCatchesUpAfterItWasDown", "TestPairWitnessLogAdmitsOnlyItsGrantedLeader", "TestPairCommissioningVouchSyncsTheOtherCopies"}},
	{"peer_tls_mismatch_unnamed", []string{"TestLoopbackPlainWitnessIsNamed", "TestLoopbackPlainControllerIsNamedBothWays", "TestLoopbackImpostorAtTheWitnessAddress", "TestPairPlainWitnessIsNamedOnTheCluster"}},
	{"peer_bind_wildcard", []string{"TestPairMemberOptions", "TestPairPeerPortBindsTheNamedAddress", "TestPairPeerPortNamesANameBoundToLoopback"}},
	// Per-box keys (#710 peer TLS PR 2a).
	{"pin_accepts_changed_key", []string{"TestAChangedKeyIsRefused", "TestThePinIsCheckedOnEveryRequest", "TestFaultOf", "TestPairATokenHolderFirstLocksOutTheRealMemberLoudly"}},
	{"pin_unsigned_accepted", []string{"TestAnUnsignedCertificateIsRefused"}},
	{"pin_any_name", []string{"TestNamesMustMatch"}},
	{"pins_unreadable_silent", []string{"TestUnreadablePinsAreMovedAside"}},
	{"pin_checked_once", []string{"TestThePinIsCheckedOnEveryRequest"}},
	{"box_key_unbound", []string{"TestTheKeyIsBoundToItsBox", "TestThePeerKeyIgnoresVirtualInterfaces"}},
	{"key_on_unknown_box", []string{"TestNoKeyWhileTheBoxIsUnknown"}},
	{"key_read_error_remakes", []string{"TestAnUnreadableKeyIsNeverReplaced"}},
	{"key_newer_version_remakes", []string{"TestAnUnreadableKeyIsNeverReplaced"}},
	{"key_no_nics_other_box", []string{"TestAnUnreadableKeyIsNeverReplaced"}},
	{"drop_key_repins", []string{"TestADroppedKeyIsNotPinnedAgain"}},
	{"refusals_undeduped", []string{"TestARefusalIsReportedOnceAMinute", "TestADroppedKeyIsNotPinnedAgain"}},
	{"witness_cert_one_inc", []string{"TestTheWitnessNamesThePairsIncarnation"}},
	{"witness_names_uncapped", []string{"TestUncountedWitnessNamesAreCapped"}},
	{"witness_call_any_inc", []string{"TestAWitnessCallTakesOnlyTheCountedIncarnation"}},
	{"drop_ban_in_memory", []string{"TestADroppedKeyStaysRefusedAcrossARestart"}},
	{"adopt_keeps_witness_pins", []string{"TestEachIncarnationIsPinnedAndAdoptionDropsTheOthers", "TestPairRebuiltWitnessIsRecopiedAndCarriesATakeover"}},
	{"reply_inc_unbound", []string{"TestLoopbackAWitnessReplyCountsOnlyUnderItsIncarnation", "TestARefusedWitnessReplyClosesTheConnections"}},
	{"witness_ops_any_inc", []string{"TestLoopbackAWitnessReplyCountsOnlyUnderItsIncarnation"}},
	{"bound_reply_keeps_conn", []string{"TestARefusedWitnessReplyClosesTheConnections"}},
	{"unbound_refusal_keeps_conn", []string{"TestAnUnboundWitnessRefusalClosesTheConnections"}},
	{"unbound_close_uncapped", []string{"TestAnUnboundWitnessRefusalClosesTheConnections"}},
	{"unbound_close_on_no_answer", []string{"TestAnUnboundWitnessRefusalClosesTheConnections"}},
	{"unadopted_reply_heard", []string{"TestLoopbackAnUnadoptedWitnessReplyReachesOnlyTheAdoption"}},
	{"witness_refusals_unbound", []string{"TestLoopbackAnUnboundWitnessRefusalRaisesNothing"}},
	{"witness_any_new_key", []string{"TestANewWitnessKeyWaitsForTheOperator"}},
	{"unpin_pending_volatile", []string{"TestPairALostWitnessAnswerNeverLifts"}},
	{"witness_key_forgotten", []string{"TestTheCountedWitnessKeyOutlivesItsName"}},
	{"unknown_key_any_name", []string{"TestWithoutTheCountedKeyOnlyTheCountedNamePins"}},
	{"allowance_any_key", []string{"TestANewWitnessKeyWaitsForTheOperator"}},
	{"allowance_kept_at_adoption", []string{"TestTheCountedWitnessKeyOutlivesItsName"}},
	{"allowance_cleared_unheard", []string{"TestAnAllowanceOutlivesAnAdoptionNotHeardHere"}},
	{"retry_skips_local_drop", []string{"TestPairAnUnpinStoppedBeforeItsDropIsFinished"}},
	{"lift_here_first", []string{"TestPairASecondUnpinLiftsTheRefusal"}},
	{"key_box_virtual_nics", []string{"TestThePeerKeyIgnoresVirtualInterfaces"}},
	{"pin_journal_unbuffered", []string{"TestPinEventsWaitForTheJournal", "TestPairPeerKeysShowInStatus"}},
	{"unpin_self", []string{"TestUnpinThroughTheCLI"}},
	{"unpin_lifts_on_retry", []string{"TestPairAnUnpinRetryNeverLifts", "TestPairALostWitnessAnswerNeverLifts"}},
	{"unpin_other_cluster", []string{"TestTheWitnessTakesAnUnpinOnlyByItsRules"}},
	{"witness_unpin_self", []string{"TestTheWitnessTakesAnUnpinOnlyByItsRules"}},
	{"unpin_newer_caller", []string{"TestTheWitnessTakesAnUnpinOnlyByItsRules"}},
	{"unpin_ignores_key", []string{"TestTheWitnessTakesAnUnpinOnlyByItsRules", "TestPairAPlainVouchAfterARefusedOneGoesAhead"}},
	{"log_witness_any_inc", []string{"TestTheLogReachesOnlyTheAdoptedWitness"}},
	{"log_witness_checked_at_dial", []string{"TestTheLogReachesOnlyTheAdoptedWitness"}},
	{"sender_unbound_vote", []string{"TestEveryHandlerBindsItsSender"}},
	{"sender_unbound_adopt", []string{"TestEveryHandlerBindsItsSender"}},
	{"sender_unbound_hello", []string{"TestEveryHandlerBindsItsSender"}},
	{"sender_unbound_dup", []string{"TestEveryHandlerBindsItsSender"}},
	{"witness_sender_unbound_vote", []string{"TestEveryHandlerBindsItsSender"}},
	{"witness_sender_unbound_dup", []string{"TestEveryHandlerBindsItsSender"}},
	{"stream_sender_unbound", []string{"TestStreamRequestsComeFromThePeer"}},
	{"witness_log_cluster_unbound", []string{"TestPairWitnessLogAdmitsOnlyItsGrantedLeader"}},
	{"log_sender_unbound", []string{"TestReplicationIsBoundToItsSender"}},
	{"handback_no_standdown_wait", []string{"TestPairHandBackEndsTheResignersHold"}},
	// The #1230 review.
	{"peer_bind_gives_up", []string{"TestPairPeerPortWaitsForItsAddress"}},
	{"peer_bind_loopback_taken", []string{"TestPairPeerPortNamesANameBoundToLoopback"}},
	{"peer_tls_handshake_unbounded", []string{"TestStalledHandshakesAreClosed"}},
	{"peer_api_guessed", []string{"TestLoopbackNewPairCommissionsAndElects", "TestPairStandbyProxiesToTheLeadersOwnAPIPort"}},
	{"peer_api_unbound", []string{"TestTheAnnouncedAPIIsBoundByTheMAC"}},
	{"peer_api_self", []string{"TestThePeersAPIIsOnlyWhatItAnnounced"}},
}

// rowPkgs are the packages a row runs instead of ./quorum/..., filtered
// to its tests: a switch outside the quorum tree.
var rowPkgs = map[string][]string{
	"lapse_ignored":                     {"./oplog"},
	"replicate_sequential":              {"./oplog"},
	"admit_skipped":                     {"./oplog"},
	"unsynced_counted":                  {"./oplog"},
	"log_loss_silent":                   {"./oplog"},
	"synced_by_unsynced_leader":         {"./oplog"},
	"resend_unchunked":                  {"./oplog"},
	"unsynced_leader_writes":            {"./oplog"},
	"behind_counted":                    {"./oplog"},
	"synced_before_commit":              {"./oplog"},
	"unsynced_self_counted":             {"./oplog"},
	"entry_unbounded":                   {"./oplog"},
	"term_not_persisted":                {"./oplog"},
	"unnamed_leader_admitted":           {"./oplog"},
	"vouch_unchecked":                   {"./oplog"},
	"vouch_alone_kept":                  {"./oplog", "./cmd/naut"},
	"mark_after_log":                    {"./oplog"},
	"vouch_alone_partial":               {"./oplog"},
	"vouch_alone_ignores_ahead":         {"./oplog"},
	"vouch_refuses_counting_ahead":      {"./oplog", "./cmd/naut"},
	"vouch_refusal_blocks":              {"./cmd/naut"},
	"reply_relay_dropped":               {"./cmd/naut"},
	"standby_probes_witness_only":       {"./quorum/...", "./cmd/naut"},
	"can_sync_stale":                    {"./oplog"},
	"placeholder_keeps_synced":          {"./oplog"},
	"unsynced_log_writes":               {"./oplog", "./cmd/naut"},
	"witness_log_unbound":               {"./cmd/naut"},
	"witness_log_term_unbound":          {"./cmd/naut"},
	"witness_log_any_incarnation":       {"./cmd/naut"},
	"accept_loss_blanket":               {"./oplog"},
	"accept_kept_after_sync":            {"./oplog"},
	"lost_log_vouch_unchecked":          {"./oplog"},
	"synced_alone_without_accept":       {"./oplog"},
	"history_not_bumped":                {"./oplog"},
	"history_after_write":               {"./oplog"},
	"history_lower_counted":             {"./oplog"},
	"history_fork_counted":              {"./oplog"},
	"fork_resolved_without_bump":        {"./oplog"},
	"superseded_not_reset":              {"./oplog"},
	"superseded_takes":                  {"./oplog"},
	"supersede_not_durable":             {"./oplog"},
	"history_moved_commits":             {"./oplog"},
	"lower_history_peer_synced":         {"./quorum/..."},
	"hello_history_ignored":             {"./cmd/naut"},
	"restart_while_leading":             {"./cmd/naut"},
	"restart_peer_unchecked":            {"./cmd/naut"},
	"restart_unsupervised":              {"./cmd/naut"},
	"restart_loops":                     {"./cmd/naut"},
	"witness_supersede_waits":           {"./cmd/naut"},
	"retained_stale_ignored":            {"./server"},
	"stale_retained_kept":               {"./cmd/naut"},
	"override_all_silent":               {"./oplog"},
	"stale_under_lock":                  {"./server"},
	"vouch_again_noop":                  {"./oplog"},
	"noop_digest_unchecked":             {"./oplog"},
	"vouch_keeps_term_synced":           {"./oplog"},
	"stale_retained_alarms_kept":        {"./runtime"},
	"takeover_keeps_first_scan":         {"./runtime"},
	"image_copy_in_scan_encodes":        {"./runtime"},
	"image_crc_unchecked":               {"./runtime"},
	"image_seq_from_scans":              {"./runtime"},
	"shadow_older_image_applied":        {"./runtime"},
	"shadow_layout_unchecked":           {"./runtime"},
	"shadow_stale_frame_kept":           {"./runtime"},
	"image_size_unbounded":              {"./runtime"},
	"shadow_kept_after_leading":         {"./runtime"},
	"stream_carries_log_intent":         {"./retain"},
	"asked_unrecorded":                  {"./quorum/..."},
	"stream_leader_unseen":              {"./quorum/..."},
	"stream_floor_unchecked":            {"./quorum/..."},
	"stream_term_unbounded":             {"./quorum/..."},
	"stream_takeover_no_flush":          {"./runtime"},
	"stream_leader_carries":             {"./runtime"},
	"stream_old_image_carried":          {"./runtime"},
	"stream_leader_saves_unmarked":      {"./runtime"},
	"stream_stale_capture_sent":         {"./runtime"},
	"stream_leader_follows":             {"./cmd/naut"},
	"stream_persist_off":                {"./cmd/naut"},
	"stream_old_term_applied":           {"./cmd/naut"},
	"stream_budget_drops":               {"./cmd/naut"},
	"stream_flush_after_io_read":        {"./runtime"},
	"stream_flush_unbounded":            {"./runtime"},
	"stream_flush_term_unkeyed":         {"./runtime"},
	"stream_failed_load_streams":        {"./runtime", "./cmd/naut"},
	"stream_store_bad_forgotten":        {"./cmd/naut"},
	"stream_sends_unled":                {"./cmd/naut"},
	"stream_flush_no_persist":           {"./cmd/naut"},
	"stream_flush_keeps_follower":       {"./cmd/naut"},
	"stream_flush_keeps_shadow":         {"./cmd/naut"},
	"stream_stale_marked_persisted":     {"./cmd/naut"},
	"stream_file_floor_unseeded":        {"./cmd/naut"},
	"stream_persist_error_hidden":       {"./cmd/naut"},
	"stream_outlives_close":             {"./cmd/naut"},
	"lease_mismatch_never_clears":       {"./quorum/...", "./cmd/naut"},
	"lease_refusal_one_way":             {"./quorum/..."},
	"lease_clear_on_wait":               {"./quorum/..."},
	"lease_condition_dropped":           {"./cmd/naut"},
	"witness_lease_no_alarm":            {"./cmd/naut"},
	"lease_check_unwarned":              {"./cmd/naut"},
	"image_tags_carry_io":               {"./runtime"},
	"image_tags_written_only":           {"./runtime"},
	"image_tag_forced_value":            {"./runtime"},
	"image_tags_at_take":                {"./runtime"},
	"copy_first_scan_only":              {"./runtime"},
	"copy_from_older_round":             {"./runtime"},
	"image_source_unhashed":             {"./runtime"},
	"shadow_tags_layout_unchecked":      {"./runtime"},
	"image_tags_retained_twice":         {"./runtime"},
	"stream_format_ignored":             {"./cmd/naut"},
	"hot_no_age_check":                  {"./runtime"},
	"hot_source_unchecked":              {"./runtime"},
	"hot_partial_tasks":                 {"./runtime"},
	"hot_globals_not_carried":           {"./runtime", "./cmd/naut"},
	"hot_timer_not_rebased":             {"./runtime"},
	"hot_age_not_elapsed":               {"./runtime"},
	"takeover_unmarked":                 {"./runtime", "./cmd/naut"},
	"takeover_flag_kept":                {"./runtime"},
	"takeover_flag_survives_step_down":  {"./runtime"},
	"peer_ready_never_set":              {"./runtime", "./cmd/naut"},
	"every_acquisition_a_takeover":      {"./quorum/...", "./cmd/naut"},
	"peer_ready_ignores_forces":         {"./cmd/naut"},
	"hold_never_starts":                 {"./runtime", "./cmd/naut"},
	"hold_release_tag_kept":             {"./runtime"},
	"hold_drivers_remember":             {"./runtime"},
	"hold_safe_unsent":                  {"./runtime"},
	"hold_not_inherited":                {"./runtime", "./cmd/naut"},
	"hold_age_restarts":                 {"./runtime", "./cmd/naut"},
	"hold_no_max":                       {"./runtime", "./cmd/naut"},
	"hold_release_tag_writable":         {"./runtime"},
	"hold_channel_writes":               {"./runtime"},
	"hold_not_imaged":                   {"./runtime", "./cmd/naut"},
	"hold_forget_nothing":               {"./modbus"},
	"hold_safe_never_lands":             {"./modbus"},
	"hold_release_at_max_ignored":       {"./runtime"},
	"hold_replace_unsafe":               {"./runtime"},
	"hold_channel_held_at_post":         {"./runtime"},
	"hold_safe_wait_restarts":           {"./runtime"},
	"ack_other_build":                   {"./cmd/naut"},
	"ack_kept_across_reconnect":         {"./cmd/naut"},
	"hot_first_scan_true":               {"./runtime"},
	"hot_late_flush_installs":           {"./runtime"},
	"hot_failed_flush_installs":         {"./runtime"},
	"hot_ticket_carried":                {"./runtime"},
	"retain_memory_only_forgotten":      {"./runtime", "./cmd/naut"},
	"hot_retain_before_checks":          {"./runtime"},
	"hot_rebase_per_task":               {"./runtime"},
	"hot_build_unchecked":               {"./cmd/naut"},
	"hot_voters_unchecked":              {"./cmd/naut"},
	"ack_old_epoch_counts":              {"./cmd/naut"},
	"current_time_based":                {"./cmd/naut"},
	"current_without_fresh_send":        {"./cmd/naut"},
	"interval_across_reconnect":         {"./cmd/naut"},
	"hot_cold_condition_never_clears":   {"./cmd/naut"},
	"handback_guard_off":                {"./quorum/...", "./cmd/naut"},
	"handback_own_record_ignored":       {"./quorum/...", "./cmd/naut"},
	"handback_keeps_renewing":           {"./quorum/...", "./cmd/naut"},
	"handback_guard_volatile":           {"./quorum/...", "./cmd/naut"},
	"handback_long_deadline":            {"./cmd/naut"},
	"handback_ineligible_peer":          {"./cmd/naut"},
	"handback_peer_guard_off":           {"./cmd/naut"},
	"handback_empty_file_good":          {"./cmd/naut"},
	"handback_peer_file_unjudged":       {"./cmd/naut"},
	"handback_taken_unalarmed":          {"./cmd/naut"},
	"handback_on_readable_store":        {"./cmd/naut"},
	"handback_when_hot":                 {"./cmd/naut"},
	"handback_store_bad_forgotten":      {"./cmd/naut"},
	"handback_restart_empty_trusted":    {"./cmd/naut"},
	"spend_failure_keeps_alone":         {"./oplog"},
	"accept_survives_reboot":            {"./oplog"},
	"superseded_lead_keeps_substitutes": {"./cmd/naut"},
	"superseded_alarms_kept":            {"./cmd/naut"},
	"empty_pair_needs_accept":           {"./oplog", "./cmd/naut"},
	"empty_pair_unchecked":              {"./oplog"},
	"set_aside_forgotten":               {"./oplog"},
	"witness_not_a_replica":             {"./cmd/naut"},
	"log_sync_not_retried":              {"./cmd/naut"},
	"spare_log_runs":                    {"./cmd/naut"},
	"ack_clear_off":                     {"./cmd/naut"},
	"peer_tls_short_token":              {"./internal/peertls", "./cmd/naut"},
	"peer_tls_server_any_client":        {"./internal/peertls", "./quorum/..."},
	"pin_accepts_changed_key":           {"./internal/peertls", "./quorum/...", "./cmd/naut"},
	"pin_unsigned_accepted":             {"./internal/peertls"},
	"pin_any_name":                      {"./internal/peertls"},
	"pins_unreadable_silent":            {"./internal/peertls"},
	"pin_checked_once":                  {"./internal/peertls"},
	"box_key_unbound":                   {"./internal/peertls", "./cmd/naut"},
	"key_on_unknown_box":                {"./internal/peertls"},
	"key_read_error_remakes":            {"./internal/peertls"},
	"key_newer_version_remakes":         {"./internal/peertls"},
	"key_no_nics_other_box":             {"./internal/peertls"},
	"drop_key_repins":                   {"./internal/peertls"},
	"refusals_undeduped":                {"./internal/peertls"},
	"witness_cert_one_inc":              {"./internal/peertls"},
	"witness_names_uncapped":            {"./internal/peertls"},
	"witness_call_any_inc":              {"./internal/peertls"},
	"drop_ban_in_memory":                {"./internal/peertls"},
	"adopt_keeps_witness_pins":          {"./internal/peertls", "./cmd/naut"},
	"reply_inc_unbound":                 {"./quorum/node"},
	"witness_ops_any_inc":               {"./quorum/node"},
	"bound_reply_keeps_conn":            {"./quorum/node"},
	"unbound_refusal_keeps_conn":        {"./quorum/node"},
	"unbound_close_uncapped":            {"./quorum/node"},
	"unbound_close_on_no_answer":        {"./quorum/node"},
	"unadopted_reply_heard":             {"./quorum/node"},
	"witness_refusals_unbound":          {"./quorum/node"},
	"witness_any_new_key":               {"./internal/peertls"},
	"unpin_pending_volatile":            {"./cmd/naut"},
	"witness_key_forgotten":             {"./internal/peertls"},
	"unknown_key_any_name":              {"./internal/peertls"},
	"allowance_any_key":                 {"./internal/peertls"},
	"allowance_kept_at_adoption":        {"./internal/peertls"},
	"allowance_cleared_unheard":         {"./internal/peertls"},
	"retry_skips_local_drop":            {"./cmd/naut"},
	"lift_here_first":                   {"./cmd/naut"},
	"key_box_virtual_nics":              {"./cmd/naut"},
	"pin_journal_unbuffered":            {"./cmd/naut"},
	"unpin_self":                        {"./cmd/naut"},
	"unpin_lifts_on_retry":              {"./cmd/naut"},
	"unpin_other_cluster":               {"./cmd/naut"},
	"witness_unpin_self":                {"./cmd/naut"},
	"unpin_newer_caller":                {"./cmd/naut"},
	"unpin_ignores_key":                 {"./cmd/naut"},
	"log_witness_any_inc":               {"./cmd/naut"},
	"log_witness_checked_at_dial":       {"./cmd/naut"},
	"stream_sender_unbound":             {"./cmd/naut"},
	"witness_log_cluster_unbound":       {"./cmd/naut"},
	"log_sender_unbound":                {"./oplog"},
	"handback_no_standdown_wait":        {"./cmd/naut"},
	"peer_tls_client_any_server":        {"./internal/peertls", "./quorum/..."},
	"peer_tls_default_off":              {"./cmd/naut"},
	"peer_tls_log_plain":                {"./cmd/naut"},
	"peer_tls_mismatch_unnamed":         {"./quorum/...", "./cmd/naut"},
	"peer_bind_wildcard":                {"./cmd/naut"},
	"peer_bind_gives_up":                {"./cmd/naut"},
	"peer_bind_loopback_taken":          {"./cmd/naut"},
	"peer_tls_handshake_unbounded":      {"./quorum/..."},
	"peer_api_guessed":                  {"./quorum/...", "./cmd/naut"},
	"peer_api_unbound":                  {"./quorum/..."},
	"peer_api_self":                     {"./quorum/..."},
	"alarm_view_ignored":                {"./cmd/naut"},
	"engine_not_reset":                  {"./server"},
	"occurrence_per_activation":         {"./alarm"},
	"ack_any_occurrence":                {"./alarm", "./server"},
	"carry_on_false":                    {"./alarm"},
	"carry_ack_after_false":             {"./alarm"},
	"carry_unbounded":                   {"./alarm"},
	"carry_through_enable":              {"./alarm"},
	"carry_wait_reset":                  {"./alarm/..."},
	"ack_clear_keeps_occurrence":        {"./server"},
	"takeover_drops_occurrences":        {"./server"},
	"ack_entry_unbound":                 {"./server"},
	"apply_ack_any_occurrence":          {"./server"},
	"carry_end_silent":                  {"./alarm", "./cmd/naut"},
	"adopt_off":                         {"./alarm", "./cmd/naut"},
	"adopt_ignores_log":                 {"./server"},
	"carry_unclamped":                   {"./alarm/..."},
	"adopt_after_suppress":              {"./alarm"},
	"carry_ack_resaved_after_false":     {"./alarm"},
	"chatter_never":                     {"./alarm/..."},
	"chatter_no_merge":                  {"./alarm/..."},
	"flood_no_hysteresis":               {"./alarm"},
	"grace_counts":                      {"./alarm"},
	"restart_counts":                    {"./alarm"},
	"first_out_repeats":                 {"./server"},
	"clock_step_unhandled":              {"./alarm"},
	"standing_sticks":                   {"./alarm"},
	"unsuppress_always_rebaselines":     {"./alarm"},
	"standing_unmarked":                 {"./alarm/..."},
	"seen_when_unmarked":                {"./alarm"},
	"chatter_pages_per_bounce":          {"./alarm/escalate"},
	"chatter_pin_latched":               {"./alarm/escalate"},
	"chatter_clock_per_wait":            {"./alarm/escalate"},
	"regroup_unbounded":                 {"./alarm/escalate"},
	"group_covers_on_failure":           {"./alarm/escalate"},
	"group_individual_ignored":          {"./alarm/escalate"},
	"notice_carries_ack":                {"./alarm/escalate"},
	"takeover_resend_individual":        {"./alarm/escalate"},
	"chatter_policy_at_start":           {"./alarm/escalate"},
	"chatter_joins_late":                {"./alarm/escalate"},
	"chatter_end_burst":                 {"./alarm/escalate"},
	"chatter_end_skips_new":             {"./alarm/escalate"},
	"group_drop_lost":                   {"./alarm/escalate"},
	"released_batch_floors":             {"./alarm/escalate"},
	"safety_case_exact":                 {"./alarm/escalate"},
	"chatter_repeats_burst":             {"./alarm/escalate"},
	"carry_detect_unclamped":            {"./alarm"},
	"takeover_drops_episodes":           {"./server", "./cmd/naut"},
	"episodes_not_synced":               {"./server", "./cmd/naut"},
	"flood_end_any":                     {"./server"},
	"episodes_stamped_now":              {"./server"},
	"carried_chatter_splits":            {"./alarm"},
	"adopt_under_mu":                    {"./server"},
	"engine_event_waits":                {"./server"},
	"clear_any_later_ack":               {"./server"},
	"clear_ack_taken_after":             {"./server"},
	"engine_event_blocks":               {"./server"},
	"clear_while_engine_holds":          {"./server"},
	"clear_without_livemu":              {"./server"},
	"reconcile_without_livemu":          {"./server"},
	"ack_clear_any_occurrence":          {"./server"},
	"engine_event_takes_livemu":         {"./server"},
	"occurrences_not_synced":            {"./server"},
	"link_replay_applied":               {"./server"},
	"link_spent_unchecked":              {"./server"},
	"link_ignores_standing":             {"./server"},
	"link_sign_in_ignored":              {"./server"},
	"link_needs_write_rule":             {"./server"},
	"link_not_forwarded":                {"./server"},
	"leader_headers_unbounded":          {"./server"},
	"link_reason_html":                  {"./server"},
	"link_acks_on_standby":              {"./server"},
	"link_session_cookie_refused":       {"./server"},
	"inbound_unsigned":                  {"./server"},
	"inbound_tries_unlimited":           {"./server"},
	"inbound_callee_unchecked":          {"./server"},
	"inbound_any_key":                   {"./server"},
	"inbound_status_on_wire":            {"./server"},
	"inbound_not_forwarded":             {"./server"},
	"voice_link_acks_here":              {"./server"},
	"inbound_replies_unlimited":         {"./server"},
	"inbound_no_global_budget":          {"./server"},
	"inbound_any_alarm":                 {"./server"},
	"inbound_unknown_as_refused":        {"./server"},
	"sms_ack_offered_while_suspended":   {"./server"},
	"inbound_stale_code_counted":        {"./server"},
	"reconcile_before_restore":          {"./server"},
	"cluster_ops_proxied":               {"./server"},
	"redundancy_same_origin":            {"./server"},
	"gate_per_batch":                    {"./hw"},
	"old_term_sent":                     {"./hw"},
	"send_no_deadline":                  {"./internal/fence", "./hw", "./modbus", "./eip", "./eip/cip", "./optommp"},
	"fence_fin":                         {"./internal/fence", "./modbus", "./eip/cip", "./redfish", "./optommp", "./opcua"},
	"fragments_unchecked":               {"./runtime", "./eip/cip", "./eip"},
	"old_term_kept":                     {"./modbus", "./optommp", "./sparkplug/host", "./opcua"},
	"set_retried":                       {"./snmp"},
	"resign_before_drain":               {"./quorum/node"},
	"resign_despite_drain_error":        {"./quorum/node"},
	"resign_despite_late_drain":         {"./quorum/node"},
	"handback_keeps_resign_hold":        {"./cmd/naut"},
	"enter_after_read":                  {"./internal/fence"},
	"drain_no_wait":                     {"./internal/fence"},
	"drain_no_reset":                    {"./internal/fence", "./optommp", "./hw"},
	"drain_no_settle":                   {"./internal/fence"},
	"host_enter_after_gate":             {"./sparkplug/host"},
	"spark_ungated":                     {"./cmd/naut"},
	"spark_no_death_on_stepdown":        {"./cmd/naut"},
	"spark_bdseq_clash":                 {"./sparkplug", "./cmd/naut"},
	"spark_publish_after_gate":          {"./sparkplug", "./cmd/naut"},
	"spark_death_watch_off":             {"./sparkplug", "./cmd/naut"},
	"spark_send_uncounted":              {"./sparkplug"},
	"spark_gate_unlatched":              {"./cmd/naut"},
	"spark_step_unfenced":               {"./cmd/naut"},
	"switch_writes_allowed":             {"./runtime"},
	"switch_scans_on":                   {"./runtime"},
	"switch_no_wait":                    {"./runtime"},
	"switch_ignores_forces":             {"./runtime"},
	"switch_ignores_hold":               {"./runtime"},
	"abort_keeps_switching":             {"./runtime", "./cmd/naut"},
	"resume_ends_switching":             {"./runtime"},
	"takeover_keeps_switching":          {"./runtime"},
	"final_keeps_round":                 {"./runtime"},
	"isdu_write_unfenced":               {"./opcua"},
	"final_image_unsent":                {"./cmd/naut"},
	"capture_ignores_switching":         {"./cmd/naut"},
	"final_ack_any_seq":                 {"./cmd/naut"},
	"switch_unready_target":             {"./cmd/naut"},
	"failback_unknown_as_known":         {"./runtime"},
	"failback_not_carried":              {"./retain"},
	"switch_ignores_io_reach":           {"./cmd/naut"},
	"failback_ready_never_resets":       {"./cmd/naut"},
	"failback_ignores_suspend":          {"./cmd/naut"},
	"failback_never_stops":              {"./cmd/naut"},
	"failback_count_never_resets":       {"./cmd/naut"},
	"failback_manual_not_suspended":     {"./cmd/naut"},
	"failback_failover_not_counted":     {"./cmd/naut"},
	"failback_undo_overwrites":          {"./cmd/naut"},
	"failback_abort_keeps_suspend":      {"./cmd/naut"},
	"failback_suspend_not_saved":        {"./cmd/naut"},
	"failback_ignores_prefer_mismatch":  {"./cmd/naut"},
	"witness_covers_below_floor":        {"./quorum"},
	"witness_reason_any":                {"./quorum"},
	"witness_floor_unwired":             {"./quorum"},
	"switch_ignores_standby_io":         {"./cmd/naut"},
	"abort_keeps_final":                 {"./cmd/naut"},
	"handback_any_epoch":                {"./quorum", "./cmd/naut"},
	"final_waits_interval":              {"./cmd/naut"},
	"no_reask":                          {"./quorum/...", "./cmd/naut"},
	"reask_new_term":                    {"./quorum/...", "./cmd/naut"},
	"reask_lease_from_last_ask":         {"./quorum/..."},
	"reask_lease_from_first_send":       {"./quorum/..."},
	"backoff_from_first_send":           {"./quorum/..."},
	"reask_declined_stays":              {"./quorum/..."},
	"grant_sent_last_reply":             {"./quorum/..."},
	"flush_next_tick":                   {"./runtime"},
	"catchup_next_tick":                 {"./runtime"},
	"edge_wait_blocks_others":           {"./runtime"},
	"leading_term_locks":                {"./runtime"},
	"wait_keeps_lead_sample":            {"./runtime"},
	"beginswitch_reads_snapshot":        {"./runtime"},
	"carry_snapshot_any_wait":           {"./runtime"},
	"save_waits_on_edge":                {"./runtime"},
	"flush_wait_after_start":            {"./runtime"},
	"abort_twice_fails":                 {"./internal/fence"},
	"resign_unchecked":                  {"./cmd/naut"},
	"final_sent_once_per_connection":    {"./cmd/naut"},
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
	// said holds each top-level test's report lines (its t.Error/t.Fatal
	// lines, "--- FAIL", a panic), the last tailLines of them, so a red
	// baseline says why, not only which test (#1120).
	said map[string][]string
}

// tailLines is how many of a failing test's report lines are kept.
const tailLines = 12

// reportLine is a line a test reports with, not a log line it printed
// along the way.
func reportLine(s string) bool {
	t := strings.TrimSpace(s)
	return strings.Contains(t, "_test.go:") || strings.HasPrefix(t, "--- FAIL") || strings.HasPrefix(t, "panic:")
}

// why gives the failing tests' report lines, indented under the table
// row, in the order the tests failed.
func (r result) why() string {
	var b strings.Builder
	for _, t := range r.failed {
		for _, l := range r.said[t] {
			b.WriteString("    " + strings.TrimSpace(l) + "\n")
		}
	}
	return b.String()
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
		if e.Action == "output" && e.Test != "" && reportLine(e.Output) {
			top, _, _ := strings.Cut(e.Test, "/")
			if r.said == nil {
				r.said = map[string][]string{}
			}
			r.said[top] = append(r.said[top], e.Output)
			if n := len(r.said[top]); n > tailLines {
				r.said[top] = r.said[top][n-tailLines:]
			}
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
	r := parse(run("", mutation{}.args()))
	b, why := baseline(r)
	for _, m := range table {
		if _, out := rowPkgs[m.name]; out && b {
			r = parse(run("", m.args())) // the rows outside the tree, green first
			b, why = baseline(r)
		}
	}
	fmt.Printf("| (baseline: the tag on, no mutation) | %s |\n", why)
	if !b {
		fmt.Print(r.why())
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
