// Command seed writes past days into an audit journal for the audit-trail
// demo (tools/audit-demo/run.sh): the journal `naut run` would have
// written had the controller run for the last few weeks.
//
// It uses the real journal writer (audit.OpenFile), with its clock set to
// each past day, and does what `naut run` does each time it starts: open
// the journal, record the retention in force, prune the files older than
// it. Between those it records a shift of synthetic operator actions. The
// lines are chained and numbered exactly as a running controller chains
// them; only the clock is in the past. The demo says so.
//
//	go run ./tools/audit-demo/seed -dir <project>/audit -days 40 -keep-days 30
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

func main() {
	dir := flag.String("dir", "audit", "the journal's directory (the manifest's audit.path)")
	days := flag.Int("days", 40, "how many past days to write, ending yesterday")
	keepDays := flag.Int("keep-days", 30, "the retention those days ran with (audit.keep-days)")
	by := flag.String("by", "nautilus", "the operating-system user each start is journalled under")
	flag.Parse()
	if err := seed(*dir, *days, *keepDays, *by, time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

// shift is one day's actions, at minutes past the day's 06:00 UTC start.
var shift = []struct {
	min    int
	user   string
	action string
	target string
	before any
	after  any
}{
	{2, "user:alice", "auth.signin", "", nil, nil},
	{15, "user:alice", "tag.write", "InflowGpm", 190.0, 210.0},
	{48, "user:alice", "alarm.ack", "HighLevel", nil, nil},
	{180, "user:bob", "auth.signin", "", nil, nil},
	{185, "user:bob", "tag.write", "P101_HOA", 2, 1},
	{240, "user:bob", "tag.write", "P101_HOA", 1, 2},
	{470, "user:alice", "auth.signout", "", nil, nil},
}

func seed(dir string, days, keepDays int, by string, now time.Time) error {
	if days < 1 || keepDays < 1 {
		return fmt.Errorf("-days and -keep-days must be at least 1")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	today := now.Truncate(24 * time.Hour)
	for d := days; d >= 1; d-- {
		start := today.Add(-time.Duration(d)*24*time.Hour + 6*time.Hour)
		clock := start
		j, err := audit.OpenFile(dir, audit.FileOptions{
			Keep: time.Duration(keepDays) * 24 * time.Hour,
			Now:  func() time.Time { return clock },
		})
		if err != nil {
			return err
		}
		if _, err := j.RecordRetention(by); err != nil {
			j.Close()
			return err
		}
		if _, err := j.Prune(); err != nil {
			j.Close()
			return err
		}
		for _, a := range shift {
			clock = start.Add(time.Duration(a.min) * time.Minute)
			src := audit.SourceHMI
			if _, err := j.Record(audit.Entry{User: a.user, Source: src, Action: a.action, Target: a.target,
				Before: a.before, After: a.after, Result: audit.ResultOK, From: "192.0.2.21"}); err != nil {
				j.Close()
				return err
			}
		}
		if err := j.Close(); err != nil {
			return err
		}
	}
	return nil
}
