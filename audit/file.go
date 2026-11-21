package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileOptions configure a FileJournal. Zero values take the defaults.
type FileOptions struct {
	// MaxSize starts a new file once the current one reaches it, besides
	// the daily one (default 64 MiB).
	MaxSize int64
	// Keep is how long files are kept before Prune removes them (default
	// 365 days). Zero keeps the default; a negative value keeps forever.
	Keep time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

const (
	defaultMaxSize = 64 << 20
	defaultKeep    = 365 * 24 * time.Hour
	filePrefix     = "audit-"
	fileSuffix     = ".jsonl"
)

// FileJournal is the durable journal: JSONL files in one directory, named
// audit-YYYYMMDD[-N].jsonl (N counts size rotations), every line chained to
// the one before it and numbered by Seq. A file's day is the UTC day it was
// opened on, never earlier than the file before it, so name order is write
// order even when entries arrive out of time order or the clock steps back.
// Safe for concurrent use.
//
// Record holds the journal's lock across the write and the fsync, so a
// stalled fsync stalls every writer behind it; coalescing and the failure
// policy belong to the caller (the server's recording seam).
type FileJournal struct {
	unlock func() // releases the directory's lock (OpenFile)
	dir    string
	opts   FileOptions
	// write and sync make an entry durable; a test replaces them to fail.
	write func(*os.File, []byte) (int, error)
	sync  func(*os.File) error

	mu     sync.Mutex
	f      *os.File
	name   string // current file's base name
	day    string // its day, YYYYMMDD
	size   int64  // bytes of complete lines in it
	head   string // "sha256:<hex>" of the last line written, "" before the first
	seq    uint64 // Seq of the last line written
	broken error  // set when a failed write could not be undone: refuse until reopened
	retSeq uint64 // Seq of the newest audit.retention entry this journal wrote, 0 before one
}

// OpenFile opens (creating) the journal in dir and continues its chain from
// the newest file's last complete line. A torn final line (bytes after the
// last newline, from a crash mid-write) is cut off and the cut is recorded
// as an audit.tail_dropped entry with the bytes' length and hash; a
// complete line is never removed, whatever it holds, so Verify can report
// it.
//
// The directory is locked for as long as the journal is open (on Linux,
// the BSDs and macOS), so a second process that opens it, such as a second
// `naut run` in the same working directory, is refused rather than
// interleaving its own chain with this one's.
func OpenFile(dir string, opts FileOptions) (*FileJournal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	j, err := openFile(dir, opts)
	if err != nil {
		unlock()
		return nil, err
	}
	j.mu.Lock()
	j.unlock = unlock
	j.mu.Unlock()
	return j, nil
}

func openFile(dir string, opts FileOptions) (*FileJournal, error) {
	if opts.MaxSize <= 0 {
		opts.MaxSize = defaultMaxSize
	}
	if opts.Keep == 0 {
		opts.Keep = defaultKeep
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	j := &FileJournal{dir: dir, opts: opts, write: (*os.File).Write, sync: (*os.File).Sync}
	files, err := listFiles(dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return j, nil
	}
	last := files[len(files)-1]
	f, err := os.OpenFile(filepath.Join(dir, last), os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	var torn []byte
	if good := bytes.LastIndexByte(data, '\n') + 1; good < len(data) {
		torn = append([]byte(nil), data[good:]...)
		if err := f.Truncate(int64(good)); err != nil {
			f.Close()
			return nil, err
		}
		data = data[:good]
	}
	if _, err := f.Seek(int64(len(data)), io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	j.f, j.name, j.size, j.day = f, last, int64(len(data)), dayOf(last)
	j.head, j.seq, err = lastLine(dir, files, len(files)-1, data)
	if err != nil {
		f.Close()
		return nil, err
	}
	if torn != nil {
		sum := sha256.Sum256(torn)
		if _, err := j.Record(Entry{User: "system", Source: SourceSystem, Action: "audit.tail_dropped",
			Target: last, Result: ResultOK, Detail: map[string]any{
				"bytes": len(torn), "sha256": hex.EncodeToString(sum[:]),
			}}); err != nil {
			j.Close()
			return nil, err
		}
	}
	return j, nil
}

// lastLine is the chain hash and Seq of the last complete line of
// files[i] (whose complete-line bytes are data) or, when it is empty, of
// the newest non-empty file before it; "" and 0 when there is none. A last
// line that is not an entry still gives its hash, so the chain goes on and
// Verify reports the damage; its Seq reads as 0.
func lastLine(dir string, files []string, i int, data []byte) (string, uint64, error) {
	for k := i; k >= 0; k-- {
		if k != i {
			d, err := os.ReadFile(filepath.Join(dir, files[k]))
			if err != nil {
				return "", 0, err
			}
			data = d[:bytes.LastIndexByte(d, '\n')+1]
		}
		data = bytes.TrimSuffix(data, []byte{'\n'})
		if len(data) == 0 {
			continue
		}
		line := data[bytes.LastIndexByte(data, '\n')+1:]
		var e Entry
		json.Unmarshal(line, &e)
		return lineHash(line), e.Seq, nil
	}
	return "", 0, nil
}

// Record appends e, numbered and chained to the line before, and returns
// once it is on disk. If the write or the fsync fails, the file is cut
// back to its last complete line, so a failed entry never leaves a line
// the next one would chain past; if even that fails, the journal refuses
// every Record until it is reopened.
func (j *FileJournal) Record(e Entry) (Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.broken != nil {
		return e, j.broken
	}
	e = fill(e, j.opts.Now())
	if err := j.rotate(e.TS); err != nil {
		return e, err
	}
	e.Seq = j.seq + 1
	e.Prev = j.head
	line, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	_, err = j.write(j.f, append(line, '\n'))
	if err == nil {
		err = j.sync(j.f)
	}
	if err != nil {
		if uerr := j.undo(); uerr != nil {
			j.broken = fmt.Errorf("audit: the journal is unusable until reopened: an entry failed (%v) and could not be cut back (%v)", err, uerr)
		}
		return e, err
	}
	j.size += int64(len(line)) + 1
	j.head = lineHash(line)
	j.seq = e.Seq
	return e, nil
}

// The server's recording seam finds batching by asserting BatchRecorder:
// if the method drifted, a batch would silently cost an fsync per entry.
var _ BatchRecorder = (*FileJournal)(nil)

// RecordBatch appends es as Record would, one after another, numbered and
// chained line by line, but in one write followed by one fsync: the batch
// is on disk when it returns, at the cost of one sync. If the write or the
// fsync fails, the file is cut back to the line before the batch, so
// either every entry is recorded or none is. The batch goes in one file:
// it is rotated for, by its first entry's time, before it is written.
func (j *FileJournal) RecordBatch(es []Entry) ([]Entry, error) {
	if len(es) == 0 {
		return nil, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.broken != nil {
		return es, j.broken
	}
	out := make([]Entry, len(es))
	now := j.opts.Now()
	for i, e := range es {
		out[i] = fill(e, now)
	}
	if err := j.rotate(out[0].TS); err != nil {
		return es, err
	}
	seq, head := j.seq, j.head
	var buf []byte
	for i := range out {
		seq++
		out[i].Seq, out[i].Prev = seq, head
		line, err := json.Marshal(out[i])
		if err != nil {
			return es, err
		}
		head = lineHash(line)
		buf = append(append(buf, line...), '\n')
	}
	_, err := j.write(j.f, buf)
	if err == nil {
		err = j.sync(j.f)
	}
	if err != nil {
		if uerr := j.undo(); uerr != nil {
			j.broken = fmt.Errorf("audit: the journal is unusable until reopened: a batch failed (%v) and could not be cut back (%v)", err, uerr)
		}
		return es, err
	}
	j.size += int64(len(buf))
	j.seq, j.head = seq, head
	return out, nil
}

// undo cuts the current file back to its last complete line after a
// failed write. Called with mu held.
func (j *FileJournal) undo() error {
	if err := j.f.Truncate(j.size); err != nil {
		return err
	}
	if _, err := j.f.Seek(j.size, io.SeekStart); err != nil {
		return err
	}
	return j.sync(j.f)
}

// rotate opens a new file when the day has moved on, the current file is
// full, or there is none yet. A file's day never goes back: an entry
// stamped earlier than the current file's day (out of order, or after the
// clock stepped back) goes in the current file. Called with mu held.
func (j *FileJournal) rotate(ts time.Time) error {
	day := ts.UTC().Format("20060102")
	if day < j.day {
		day = j.day
	}
	if j.f != nil && day == j.day && j.size < j.opts.MaxSize {
		return nil
	}
	name := filePrefix + day + fileSuffix
	if j.f != nil && day == j.day {
		name = nextName(j.name, day)
	}
	for {
		if _, err := os.Stat(filepath.Join(j.dir, name)); os.IsNotExist(err) {
			break
		}
		name = nextName(name, day)
	}
	f, err := os.OpenFile(filepath.Join(j.dir, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := j.syncDir(); err != nil {
		f.Close()
		os.Remove(filepath.Join(j.dir, name))
		return err
	}
	if j.f != nil {
		j.f.Close()
	}
	j.f, j.name, j.day, j.size = f, name, day, 0
	return nil
}

// syncDir makes a new file's directory entry durable. Windows cannot sync
// a directory (as retain/file.go notes), so there it is skipped.
func (j *FileJournal) syncDir() error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(j.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// nextName is the name after cur on day: audit-DAY.jsonl, then -1, -2, …
func nextName(cur, day string) string {
	n := 0
	base := strings.TrimSuffix(strings.TrimPrefix(cur, filePrefix), fileSuffix)
	if i := strings.IndexByte(base, '-'); i >= 0 && base[:i] == day {
		fmt.Sscanf(base[i+1:], "%d", &n)
	}
	return fmt.Sprintf("%s%s-%d%s", filePrefix, day, n+1, fileSuffix)
}

// Head is the hash and Seq of the last line written: the anchor a copy
// kept elsewhere is compared against.
func (j *FileJournal) Head() (string, uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.head, j.seq
}

// Dir is the journal's directory.
func (j *FileJournal) Dir() string { return j.dir }

// Close closes the current file.
func (j *FileJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var err error
	if j.f != nil {
		err = j.f.Close()
		j.f = nil
	}
	if j.unlock != nil {
		j.unlock()
		j.unlock = nil
	}
	return err
}

// ActionRetention is the entry recording the retention in force: naut run
// writes one at every journal open (RecordRetention), and every
// audit.prune carries the same keep, so a check against an anchored copy
// can tell an honest prune from a forged one (VerifyAnchored).
const ActionRetention = "audit.retention"

// ActionPrune is the entry recording a pruned file.
const ActionPrune = "audit.prune"

// keepSeconds is how a retention is journalled: whole seconds, -1 for
// keeping every file.
func keepSeconds(keep time.Duration) int64 {
	if keep < 0 {
		return -1
	}
	return int64(keep / time.Second)
}

// RecordRetention journals the retention in force (the journal's Keep) as
// an audit.retention entry. by names who started the journal (the
// process's user), for a report that lists a lowered retention.
func (j *FileJournal) RecordRetention(by string) (Entry, error) {
	d := map[string]any{"keep": keepSeconds(j.opts.Keep)}
	if by != "" {
		d["by"] = by
	}
	e, err := j.Record(Entry{User: "system", Source: SourceSystem, Action: ActionRetention, Result: ResultOK, Detail: d})
	if err != nil {
		return e, err
	}
	j.mu.Lock()
	j.retSeq = e.Seq
	j.mu.Unlock()
	return e, nil
}

// RetentionSeq is the Seq of the newest audit.retention entry this
// journal wrote, 0 before one.
func (j *FileJournal) RetentionSeq() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.retSeq
}

// Prune removes files whose day is older than Keep, never the current one,
// and records each removal as an "audit.prune" entry carrying the removed
// file's last line hash and Seq, so Verify knows the chain legitimately
// begins there, and the keep it pruned by. It returns the files removed.
//
// A journal that recorded its retention (RecordRetention) records it again
// before removing the file that holds that record, so the retention every
// prune names is never pruned away from the journal itself.
func (j *FileJournal) Prune() ([]string, error) { return j.prune(false) }

// PruneUnanchored prunes as Prune does, marking each audit.prune
// unanchored: naut run held a lowered retention's first prune for its
// anchor, and the disk filled first. Verify reports such a prune as an
// UNANCHORED LOWERING.
func (j *FileJournal) PruneUnanchored() ([]string, error) { return j.prune(true) }

func (j *FileJournal) prune(unanchored bool) ([]string, error) {
	if j.opts.Keep < 0 {
		return nil, nil
	}
	cutoff := j.opts.Now().UTC().Add(-j.opts.Keep).Format("20060102")
	files, err := listFiles(j.dir)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	current := j.name
	j.mu.Unlock()
	var removed []string
	for i, name := range files {
		if name == current || dayOf(name) >= cutoff {
			continue
		}
		data, err := os.ReadFile(filepath.Join(j.dir, name))
		if err != nil {
			return removed, err
		}
		last, seq, err := lastLine(j.dir, files, i, data[:bytes.LastIndexByte(data, '\n')+1])
		if err != nil {
			return removed, err
		}
		if r := j.RetentionSeq(); r > 0 && r <= seq {
			if _, err := j.RecordRetention(""); err != nil {
				return removed, err
			}
		}
		// Record first: a crash between the two leaves a record of a
		// removal that did not happen, the safe direction.
		if _, err := j.Record(Entry{User: "system", Source: SourceSystem, Action: ActionPrune,
			Target: name, Result: ResultOK, Detail: pruneDetail(last, seq, keepSeconds(j.opts.Keep), unanchored)}); err != nil {
			return removed, err
		}
		if err := os.Remove(filepath.Join(j.dir, name)); err != nil {
			return removed, err
		}
		removed = append(removed, name)
	}
	return removed, nil
}

func pruneDetail(last string, seq uint64, keep int64, unanchored bool) map[string]any {
	d := map[string]any{"last": last, "seq": seq, "keep": keep}
	if unanchored {
		d["unanchored"] = true
	}
	return d
}

// listFiles is the journal's files in chain order: by day, then rotation.
func listFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && IsJournalFile(n) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(a, b int) bool { return fileKey(out[a]) < fileKey(out[b]) })
	return out, nil
}

// IsJournalFile reports whether name (a base name) is one of the
// journal's files, audit-YYYYMMDD[-N].jsonl: what `naut build` leaves out
// of a built binary, wherever in the project it sits.
func IsJournalFile(name string) bool {
	return strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix) && dayOf(name) != ""
}

// dayOf is a file name's YYYYMMDD, or "" when it is not a journal file.
func dayOf(name string) string {
	base := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
	if len(base) < 8 {
		return ""
	}
	if _, err := time.Parse("20060102", base[:8]); err != nil {
		return ""
	}
	return base[:8]
}

// fileKey orders files by day, then rotation number.
func fileKey(name string) string {
	base := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
	n := 0
	if len(base) > 9 {
		fmt.Sscanf(base[9:], "%d", &n)
	}
	return fmt.Sprintf("%s-%09d", base[:8], n)
}

// lineHash is the chain hash of one line's bytes, without its newline.
func lineHash(line []byte) string {
	sum := sha256.Sum256(line)
	return "sha256:" + hex.EncodeToString(sum[:])
}
