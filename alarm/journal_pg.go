package alarm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	// Imported non-blank because Query binds slices with pq.Array; its
	// driver registers itself via init() either way. Same import, same
	// reason, as hist.
	"github.com/lib/pq"
)

// PGJournal is a Postgres-backed Journal.
//
// It reuses hist's migration idiom exactly — one idempotent DDL string at
// open, no version table, no migration files — because an alarm journal
// that needs a migration step to start is an alarm journal that will one
// day not start.
//
// Appends are batched on a background goroutine: Append is called from the
// scan goroutine, and a scan that waits on a database round trip is a
// controller that stops controlling. A full queue drops with a counter,
// which the ring journal in front of this makes survivable.
type PGJournal struct {
	db      *sql.DB
	ownsDB  bool
	ch      chan Event
	flushCh chan chan struct{}
	done    chan struct{}
	wg      sync.WaitGroup

	mu      sync.Mutex
	dropped uint64
	lastErr error

	closeOnce sync.Once
}

// pgQueue and pgBatch size the hand-off. A batch of 256 is one round trip
// per few hundred events, which a flapping storm reaches and a normal plant
// never does.
const (
	pgQueue    = 4096
	pgBatch    = 256
	pgInterval = 250 * time.Millisecond
)

// alarmEventsDDL is the whole schema. Kept as one string, run at every
// open, so a fresh database and a database the engine has run against
// before are the same case.
const alarmEventsDDL = `
CREATE TABLE IF NOT EXISTS alarm_events (
  ts timestamptz NOT NULL, id text NOT NULL, name text, kind text NOT NULL,
  priority text, site text, state text, "by" text);
CREATE INDEX IF NOT EXISTS alarm_events_site_ts ON alarm_events (site, ts DESC);
CREATE INDEX IF NOT EXISTS alarm_events_ts ON alarm_events (ts DESC);`

// NewPostgres connects, migrates and starts the writer. dsn is a standard
// postgres DSN.
func NewPostgres(dsn string) (*PGJournal, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	// A journal is a light writer and a light reader; a handful of
	// connections is plenty and keeps it from crowding out the historian
	// sharing the same database.
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	j, err := NewPostgresDB(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	j.ownsDB = true
	return j, nil
}

// NewPostgresDB is NewPostgres over a database handle the caller already
// has — the historian's, normally, since alarm_events and samples belong in
// the same place. Close does not close a handle it was handed.
func NewPostgresDB(db *sql.DB) (*PGJournal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, alarmEventsDDL); err != nil {
		return nil, fmt.Errorf("alarm_events migration: %w", err)
	}
	j := &PGJournal{
		db:      db,
		ch:      make(chan Event, pgQueue),
		flushCh: make(chan chan struct{}),
		done:    make(chan struct{}),
	}
	j.wg.Add(1)
	go j.writer()
	return j, nil
}

// Append enqueues an event. It never blocks and never returns a database
// error — the error surfaces through Err, and the drop through Dropped.
// Appending after Close is a no-op rather than a panic; shutdown ordering
// between the scan loop and its sinks is not worth a crash.
func (j *PGJournal) Append(e Event) error {
	select {
	case <-j.done:
		return nil
	default:
	}
	select {
	case j.ch <- e:
		return nil
	default:
		j.mu.Lock()
		j.dropped++
		j.mu.Unlock()
		return nil
	}
}

// Dropped counts events the write queue could not accept.
func (j *PGJournal) Dropped() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.dropped
}

// Err is the last write error, or nil. Reading it is how a caller notices
// that a database has gone away without Append having lied about it.
func (j *PGJournal) Err() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastErr
}

// Flush blocks until everything enqueued so far has been written. Tests
// need it; so does a clean shutdown.
func (j *PGJournal) Flush() error {
	ack := make(chan struct{})
	select {
	case j.flushCh <- ack:
		<-ack
	case <-j.done:
	}
	return j.Err()
}

func (j *PGJournal) writer() {
	defer j.wg.Done()
	batch := make([]Event, 0, pgBatch)
	t := time.NewTicker(pgInterval)
	defer t.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := j.insert(batch); err != nil {
			j.mu.Lock()
			j.lastErr = err
			j.mu.Unlock()
		}
		batch = batch[:0]
	}
	for {
		select {
		case e, ok := <-j.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= pgBatch {
				flush()
			}
		case ack := <-j.flushCh:
			// Drain whatever is already queued so Flush means "everything
			// Append has accepted", not "everything the batch happened to
			// hold".
			for drained := false; !drained; {
				select {
				case e, ok := <-j.ch:
					if !ok {
						drained = true
						continue
					}
					batch = append(batch, e)
				default:
					drained = true
				}
			}
			flush()
			close(ack)
		case <-t.C:
			flush()
		}
	}
}

// insert writes a batch as one multi-row INSERT, so a storm costs one round
// trip per few hundred events rather than one per event.
func (j *PGJournal) insert(batch []Event) error {
	var b strings.Builder
	b.WriteString(`INSERT INTO alarm_events (ts, id, name, kind, priority, site, state, "by") VALUES `)
	args := make([]any, 0, len(batch)*8)
	for i, e := range batch {
		if i > 0 {
			b.WriteString(",")
		}
		n := i * 8
		fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8)
		args = append(args, e.Time().UTC(), e.ID, e.Name, e.Kind,
			e.Priority.String(), e.Site, e.State, e.By)
	}
	_, err := j.db.Exec(b.String(), args...)
	return err
}

func (j *PGJournal) Query(from, to time.Time, f Filter) ([]Event, error) {
	q := `SELECT ts, id, name, kind, priority, site, state, "by" FROM alarm_events WHERE true`
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(clause, len(args))
	}
	if !from.IsZero() {
		add(" AND ts >= $%d", from.UTC())
	}
	if !to.IsZero() {
		add(" AND ts <= $%d", to.UTC())
	}
	// pq.Array binds each slice as a real parameter — never rendered into
	// the SQL, so a site name with a quote in it is data, not syntax.
	if len(f.Sites) > 0 {
		add(" AND site = ANY($%d)", pq.Array(f.Sites))
	}
	if len(f.IDs) > 0 {
		add(" AND id = ANY($%d)", pq.Array(f.IDs))
	}
	if len(f.Kinds) > 0 {
		add(" AND kind = ANY($%d)", pq.Array(f.Kinds))
	}
	if len(f.Priorities) > 0 {
		add(" AND priority = ANY($%d)", pq.Array(f.Priorities))
	}
	add(" ORDER BY ts DESC LIMIT $%d", f.limit())

	rows, err := j.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Event, 0, 64)
	for rows.Next() {
		var (
			ts                                  time.Time
			id, kind                            string
			name, priority, site, state, byUser sql.NullString
		)
		if err := rows.Scan(&ts, &id, &name, &kind, &priority, &site, &state, &byUser); err != nil {
			return nil, err
		}
		e := Event{
			TS: ts.UnixMilli(), ID: id, Kind: kind,
			Name: name.String, Site: site.String, State: state.String, By: byUser.String,
		}
		if p, err := ParsePriority(priority.String); err == nil {
			e.Priority = p
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes events older than the retention window. A flapping storm
// across a couple of thousand alarms writes thousands of rows a minute, so
// this is not optional the way it might be for a hand-fed table — call it
// on a ticker beside hist.Prune.
func (j *PGJournal) Prune(olderThan time.Duration) error {
	_, err := j.db.Exec(`DELETE FROM alarm_events WHERE ts < $1`, time.Now().Add(-olderThan))
	return err
}

// Span reports the range and count of what is stored, for a status line.
func (j *PGJournal) Span() (first, last time.Time, count int64, err error) {
	err = j.db.QueryRow(`SELECT min(ts), max(ts), count(*) FROM alarm_events`).
		Scan(&nullTime{&first}, &nullTime{&last}, &count)
	return
}

// Close stops the writer, flushing what is queued, and closes the database
// handle only if this journal opened it.
func (j *PGJournal) Close() error {
	var err error
	j.closeOnce.Do(func() {
		close(j.done)
		close(j.ch)
		j.wg.Wait()
		if j.ownsDB {
			err = j.db.Close()
		}
	})
	return err
}

// nullTime lets min()/max() return NULL on an empty table without error.
type nullTime struct{ t *time.Time }

func (n *nullTime) Scan(v any) error {
	if v == nil {
		return nil
	}
	if t, ok := v.(time.Time); ok {
		*n.t = t
	}
	return nil
}
