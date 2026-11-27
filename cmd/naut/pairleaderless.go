package main

import (
	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

// PairLeaderlessAlarmID is the system alarm a pair member raises while it
// sees no leader (#1436): the pair may be running no outputs, and the
// alarm engine, which evaluates after each scan, evaluates nothing. Raised
// and paged by one member (quorum/node leaderlessalarm.go), on an engine
// of its own (project.PairAlarm). Not acknowledged: it ends when a leader
// is seen.
const PairLeaderlessAlarmID = "nautilus.pair-leaderless"

// leaderlessAlarm is the pair member m's no-leader alarm.
func leaderlessAlarm(m *pairMember) *project.PairAlarm {
	n := m.q.Node()
	active := func() bool { r, _ := n.LeaderlessAlarm(); return r }
	episode := func() uint64 { _, ep := n.LeaderlessAlarm(); return ep }
	return &project.PairAlarm{
		Def: alarm.Def{
			ID: PairLeaderlessAlarmID, Tag: "__nautilus.pair-leaderless",
			Name:     "Redundancy: this controller sees no leader, so the pair may be running no outputs (the cause: naut redundancy status, or this controller's log)",
			Priority: alarm.Critical, Class: "system",
			AutoClear: true,
		},
		Active: active, Episode: episode,
	}
}

// leaderlessWord is what m's hello answers say of its no-leader alarm
// (node.Options.LeaderlessWord): it has one, and its pages are not
// failing. Without one (no alarms: section, or not built yet) it says
// nothing, and the peer raises the alarm too.
func (m *pairMember) leaderlessWord() bool {
	a := m.pairAlarms.Load()
	return a != nil && !a.Failing()
}

// startPairAlarms is startAlarms on a pair member (pair nil: none): with
// its no-leader alarm, whose engine the member's hello word then reads.
func startPairAlarms(proj *project.Project, rt *runtime.Runtime, pair *pairMember, sys ...project.SystemAlarm) (*project.Alarms, error) {
	if pair != nil && !mutNaut("pair_alarm_none") {
		proj.PairAlarm = leaderlessAlarm(pair)
	}
	alarms, err := startAlarms(proj, rt, sys...)
	if err == nil && pair != nil && alarms != nil && alarms.Pair != nil {
		pair.pairAlarms.Store(alarms.Pair)
	}
	return alarms, err
}
