package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/auditpg"
	"github.com/joyautomation/nautilus/internal/project"
)

// startAuditCopy starts the journal's copy in Postgres when the manifest
// sets audit.sink: postgres (#709 PR 9): a tailer that reads the journal's
// files into the audit_entries table every second, never past the
// journal's durable head, and catches up after any outage. It returns the
// tailer (nil without a sink) and a stop function that ends it, then
// copies what the shutdown wrote (system.stop and the seam's last
// entries) and closes the database handle.
//
// A sink without a connection string is an error: the manifest asked for
// a copy that cannot be made. A database that is down is not: the copy
// waits for it, and the controller runs.
//
// The copy also anchors the journal's head (#709 PR 9b): at once, then
// every audit.anchor-every when it moved, after every copied prune, and at
// stop. It checks its own database role and says when the anchors prove
// nothing because that role could rewrite them. The returned gate holds the
// run's first prune while a lowered retention is not yet anchored
// (pruneGate).
func startAuditCopy(ctx context.Context, proj *project.Project, j *audit.FileJournal, getenv func(string) string, stderr io.Writer) (*audit.Tailer, func(), func(context.Context) gateAnswer, error) {
	dsn, err := proj.AuditCopyDSN(getenv)
	if err != nil || dsn == "" {
		return nil, func() {}, nil, err
	}
	name := auditController(proj, getenv)
	store, err := auditpg.Open(dsn, name)
	if err != nil {
		return nil, func() {}, nil, fmt.Errorf("audit.sink: %w", err)
	}
	dir, keep := proj.AuditJournal()
	t := audit.NewTailer(dir, store, j.Head)
	t.AnchorTo(store, proj.AuditAnchorEvery())
	t.OnChange = func(st audit.CopyStatus) {
		switch st.State {
		case audit.CopyCurrent:
			fmt.Fprintf(stderr, "audit: the copy in Postgres is current (seq %d)\n", st.Copied)
		case audit.CopyBehind:
			if st.LastError != "" {
				fmt.Fprintf(stderr, "audit: the copy in Postgres is behind (seq %d of %d): %s; it catches up from the journal's files\n", st.Copied, st.Head, st.LastError)
			}
		case audit.CopyStopped:
			fmt.Fprintf(stderr, "audit: ERROR: %s\n", st.LastError)
		}
	}
	fmt.Fprintf(stderr, "audit: copying the journal to Postgres as controller %q\n", name)
	// Its own context: stop may run on an early return, before the
	// signal that ends ctx.
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.Run(rctx, time.Second)
	}()
	go checkCopyRole(rctx, store, t, stderr)
	stop := func() {
		cancel()
		<-done
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		t.Step(dctx)
		if err := t.AnchorNow(dctx, audit.AnchorStop); err != nil {
			fmt.Fprintf(stderr, "audit: the stop anchor was not written: %v\n", err)
		}
		store.Close()
	}
	cur := audit.KeepSeconds(keep)
	gate := func(ctx context.Context) gateAnswer {
		lowered := j.Lowered()
		if k, ok, err := store.AnchoredKeep(ctx); err == nil {
			// The database knows what was anchored last: compare with that.
			lowered = ok && audit.ShorterKeep(cur, k)
		}
		return gateAnswer{lowered: lowered, anchored: t.Status().Anchored >= j.RetentionSeq()}
	}
	return t, stop, gate, nil
}

// checkCopyRole asks the database, once it answers, whether the copy's own
// role could rewrite the copy or the anchors, and says so when it could:
// the anchors then prove nothing against someone holding that role.
func checkCopyRole(ctx context.Context, store *auditpg.Store, t *audit.Tailer, stderr io.Writer) {
	for {
		role, err := store.Role(ctx)
		var c audit.WriterCheck
		if err == nil {
			c, err = store.Protection(ctx, role)
		}
		if err == nil {
			if !c.Protected {
				msg := fmt.Sprintf("anchor not protected: it proves nothing against someone with role %q (%s); run naut audit pg-schema --role %s", role, c.Why, role)
				t.SetUnprotected(msg)
				fmt.Fprintf(stderr, "audit: WARNING: %s\n", msg)
			}
			return
		}
		if ctx.Err() == nil {
			fmt.Fprintf(stderr, "audit: checking the copy's database role: %v (retried)\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// gateAnswer is what the prune gate knows now.
type gateAnswer struct {
	lowered  bool // the run's retention is shorter than the last anchored one
	anchored bool // the run's retention entry is anchored
}

// pruneFullAt is how full the journal's disk may get while a lowered
// retention's first prune waits for its anchor: past it, the prune goes
// ahead, marked unanchored.
const pruneFullAt = 0.90

// waitToPrune holds the run's first prune while the retention is lowered
// and its record not yet anchored, so the anchors show the lowering came
// before the prune (verify reads a lowering anchored only with its prune
// as forged). An equal or longer retention prunes at once. The wait is
// bounded by the disk: past pruneFullAt full (or when that cannot be
// told) it returns unanchored, and the prune is journalled as such. It
// returns false when ctx ends.
func waitToPrune(ctx context.Context, gate func(context.Context) gateAnswer, used func() (float64, error), every time.Duration, stderr io.Writer) (go_, unanchored bool) {
	said := false
	for {
		a := gate(ctx)
		if !a.lowered || a.anchored {
			if said {
				fmt.Fprintln(stderr, "audit: the lowered retention is anchored; pruning")
			}
			return true, false
		}
		u, err := used()
		if err != nil || u >= pruneFullAt {
			why := fmt.Sprintf("the disk is %.0f%% full", u*100)
			if err != nil {
				why = "the disk's use cannot be read (" + err.Error() + ")"
			}
			fmt.Fprintf(stderr, "audit: WARNING: pruning by a LOWERED retention that is not anchored in Postgres yet, because %s: the prune is journalled unanchored and naut audit verify --anchor reports it as an UNANCHORED LOWERING\n", why)
			return true, true
		}
		if !said {
			fmt.Fprintln(stderr, "audit: the retention was lowered: the first prune waits until Postgres anchors the lowering (files are kept meanwhile)")
			said = true
		}
		select {
		case <-ctx.Done():
			return false, false
		case <-time.After(every):
		}
	}
}

// auditController is the name the copy's rows carry, so several
// controllers can share one database: the project's name, and on a
// redundant pair the member's (each keeps its own journal and chain). In a
// cluster (POD_NAME set), the pod's name: run it as a StatefulSet, so the
// name and the journal's volume stay with the replica.
func auditController(proj *project.Project, getenv func(string) string) string {
	name := proj.Name
	if proj.Redundancy.Pair() {
		if node := strings.ToLower(strings.TrimSpace(getenv("NAUTILUS_NODE"))); node != "" {
			return name + "/" + node
		}
	}
	if pod := getenv("POD_NAME"); pod != "" {
		return name + "/" + pod
	}
	return name
}
