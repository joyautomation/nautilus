package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/notes"
	"github.com/joyautomation/nautilus/oplog"
)

const notesUsage = `naut notes — the operator notes and shift log (#720)

Usage:
  naut notes export [--project dir] [--state-dir dir] [--format json|csv] [--since <RFC3339|date>] [--until <...>]

Writes every note on stdout, read from the controller's state directory
(it works with the controller stopped): a controller on its own keeps them
in notes.jsonl; a redundant pair's are in its operator-intent log
(oplog.jsonl), which this replays as the controller does at start.

  json   notes with every revision, oldest first
  csv    one row per revision: note, rev, at, by, principal, target kind,
         id, occurrence, kind, re, retracted, reason, text, history

A pair whose log was replaced (naut redundancy vouch) keeps the old log aside:
its notes are exported too, flagged "superseded" (json) or in the history
column (csv: "superseded <when moved aside>"), never mixed into the live ones.

A note is never deleted: a retracted one is exported with its history, and
every revision's text is there. The audit journal also holds each write's
text (note.add, note.revise, note.retract).
`

// runNotes is naut notes in a project checkout.
func runNotes(args []string) int {
	return notesMain(args, func(dir string) (*project.Manifest, string, error) {
		m, err := project.ReadManifest(os.DirFS(dir), "")
		return m, dir, err
	}, true, os.Getenv, os.Stdout, os.Stderr)
}

// loadNotes reads the notes a state directory holds: the file of a
// controller on its own, else a pair's log replayed. none says there is
// neither (a controller that never wrote one).
func loadNotes(sd string, stderr io.Writer) (all []exportNote, source string, err error) {
	file, logf := filepath.Join(sd, NotesFile), filepath.Join(sd, pairLogFile)
	fi, ferr := os.Stat(file)
	li, lerr := os.Stat(logf)
	// a controller moved from standing alone to a pair has both: the one
	// written last is the one in use
	if ferr == nil && lerr == nil {
		if li.ModTime().After(fi.ModTime()) {
			ferr = os.ErrNotExist
			fmt.Fprintf(stderr, "naut notes: %s and %s are both in %s; reading the pair's log, written last (a note written before the move is in the file: export it too)\n", NotesFile, pairLogFile, sd)
		} else {
			lerr = os.ErrNotExist
			fmt.Fprintf(stderr, "naut notes: %s and %s are both in %s; reading the file, written last\n", NotesFile, pairLogFile, sd)
		}
	}
	if ferr == nil {
		s, err := notes.ReadFile(file) // read-only: a running controller holds the file
		if err != nil {
			return nil, "", err
		}
		return live(s.All()), file, nil
	}
	if lerr == nil {
		es, err := oplog.ReadCommitted(logf)
		if err != nil {
			return nil, "", err
		}
		s, bad := notes.Replay(es)
		if bad > 0 {
			fmt.Fprintf(stderr, "naut notes: %d note entries in %s could not be applied (a revision that lost a race, a gap, or an entry with no readable body); the rest are shown\n", bad, logf)
		}
		out := live(s.All())
		// the history a vouch replaced, kept aside: a record of what was
		// written then, flagged, never merged into the live notes. A note
		// the live history holds too is not repeated.
		// the one the log's mark names (as the server shows it), else the newest by name
		old, acceptedBy := oplog.SupersededInMark(filepath.Join(sd, pairLogMark))
		if old == "" {
			old = oplog.NewestSuperseded(logf)
		}
		if old != "" {
			if oes, err := oplog.ReadCommitted(old); err == nil {
				os, _ := notes.Replay(oes)
				moved := ""
				if i := strings.LastIndex(old, ".superseded-"); i >= 0 {
					if u, err := strconv.ParseInt(old[i+len(".superseded-"):], 10, 64); err == nil {
						moved = time.Unix(u, 0).UTC().Format(time.RFC3339)
					}
				}
				for _, n := range os.All() {
					if _, ok := s.Get(n.ID); !ok {
						out = append(out, exportNote{Note: n, Superseded: true, MovedAside: moved, AcceptedBy: acceptedBy})
					}
				}
			} else {
				fmt.Fprintf(stderr, "naut notes: %s (a replaced history) could not be read: %v\n", old, err)
			}
		}
		return out, logf, nil
	}
	return nil, "", nil
}

// exportNote is a note as exported: the note, and whether it comes from a
// log history that was replaced (naut redundancy vouch), which is a record
// and not part of the live notes.
type exportNote struct {
	notes.Note
	Superseded bool   `json:"superseded,omitempty"`
	MovedAside string `json:"movedAside,omitempty"`
	AcceptedBy string `json:"acceptedBy,omitempty"`
}

func live(ns []notes.Note) []exportNote {
	out := make([]exportNote, len(ns))
	for i, n := range ns {
		out[i] = exportNote{Note: n}
	}
	return out
}

func notesMain(args []string, load func(dir string) (*project.Manifest, string, error), devFallback bool,
	getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("naut notes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, notesUsage) }
	projectDir := fs.String("project", ".", "project directory")
	stateDir := fs.String("state-dir", "", "the controller's state directory")
	format := fs.String("format", "json", "json or csv")
	since := fs.String("since", "", "only notes first written at or after this time (RFC 3339, or a date)")
	until := fs.String("until", "", "only notes first written before this time")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// Flags after the subcommand too, as the guide writes them
	// (`naut notes export --format csv`): the set parses what follows
	// `export` with the same flags, and a stray word after them is still a
	// usage error.
	words := fs.Args()
	if len(words) > 1 && words[0] == "export" {
		if err := fs.Parse(words[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		words = append([]string{"export"}, fs.Args()...)
	}
	if len(words) == 1 && words[0] == "help" {
		fmt.Fprint(stdout, notesUsage)
		return 0
	}
	if len(words) != 1 || words[0] != "export" {
		fmt.Fprint(stderr, notesUsage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "naut notes: %v\n", err)
		return 1
	}
	if *format != "json" && *format != "csv" {
		return fail(errors.New("--format is json or csv"))
	}
	parse := func(flagName, v string) (int64, error) {
		if v == "" {
			return 0, nil
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UnixMilli(), nil
			}
		}
		return 0, fmt.Errorf("--%s %q is an RFC 3339 time or a date (2026-11-22)", flagName, v)
	}
	from, err := parse("since", *since)
	if err != nil {
		return fail(err)
	}
	to, err := parse("until", *until)
	if err != nil {
		return fail(err)
	}
	m, dir, err := load(*projectDir)
	if err != nil && *stateDir == "" {
		return fail(fmt.Errorf("%w (or pass --state-dir)", err))
	}
	sd, name := *stateDir, ""
	if m != nil {
		name = m.Name
		if sd == "" {
			sd = m.AuthStateDir()
			// a pair keeps its log where it keeps its voter state
			if m.Redundancy != nil && m.Redundancy.StateDir != "" {
				if _, serr := os.Stat(filepath.Join(m.Redundancy.StateDir, pairLogFile)); serr == nil {
					sd = m.Redundancy.StateDir
				}
			}
		}
	}
	if sd, err = controllerStateDir("the operator notes", sd, name, dir, devFallback, getenv); err != nil {
		return fail(err)
	}
	all, source, err := loadNotes(sd, stderr)
	if err != nil {
		return fail(err)
	}
	if source == "" {
		fmt.Fprintf(stderr, "naut notes: no notes file or operator-intent log in %s: nothing has been written\n", sd)
	}
	var out []exportNote
	for _, n := range all {
		if at := n.First().At; (from > 0 && at < from) || (to > 0 && at >= to) {
			continue
		}
		out = append(out, n)
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if out == nil {
			out = []exportNote{}
		}
		if err := enc.Encode(out); err != nil {
			return fail(err)
		}
		return 0
	}
	w := csv.NewWriter(stdout)
	_ = w.Write([]string{"note", "rev", "at", "by", "principal", "target_kind", "target_id", "occurrence", "kind", "re", "retracted", "reason", "text", "history"})
	for _, n := range out {
		for _, r := range n.Revisions {
			kind := r.Kind
			if kind == "" {
				kind = notes.NoteNote
			}
			history := "live"
			if n.Superseded {
				history = "superseded " + n.MovedAside
				if n.AcceptedBy != "" {
					history += " (accepted by " + n.AcceptedBy + ")"
				}
			}
			_ = w.Write([]string{
				r.Note, strconv.Itoa(r.Rev), time.UnixMilli(r.At).UTC().Format(time.RFC3339), csvSafe(r.By), csvSafe(r.Principal),
				csvSafe(r.Target.Kind), csvSafe(r.Target.ID), csvSafe(r.Target.Occurrence), kind, csvSafe(r.Re), strconv.FormatBool(r.Retract), csvSafe(r.Reason), csvSafe(r.Text), history,
			})
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fail(err)
	}
	return 0
}

// csvSafe keeps a spreadsheet from running a note: a cell that starts with
// =, +, - or @ is read as a formula, so such text gets a leading quote mark.
func csvSafe(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r') {
		return "'" + s
	}
	return s
}
