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
func startAuditCopy(ctx context.Context, proj *project.Project, j *audit.FileJournal, getenv func(string) string, stderr io.Writer) (*audit.Tailer, func(), error) {
	dsn, err := proj.AuditCopyDSN(getenv)
	if err != nil || dsn == "" {
		return nil, func() {}, err
	}
	name := auditController(proj, getenv)
	store, err := auditpg.Open(dsn, name)
	if err != nil {
		return nil, func() {}, fmt.Errorf("audit.sink: %w", err)
	}
	dir, _ := proj.AuditJournal()
	t := audit.NewTailer(dir, store, j.Head)
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
	stop := func() {
		cancel()
		<-done
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		t.Step(dctx)
		store.Close()
	}
	return t, stop, nil
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
