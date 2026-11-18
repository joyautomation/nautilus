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
// audit-YYYYMMDD[-N].jsonl by UTC day (N counts size rotations), every line
// chained to the one before it. Safe for concurrent use.
type FileJournal struct {
	dir  string
	opts FileOptions
	// sync makes a write durable; a test replaces it to count calls.
	sync func(*os.File) error

	mu   sync.Mutex
	f    *os.File
	name string // current file's base name
	day  string // its UTC day, YYYYMMDD
	size int64
	head string // "sha256:<hex>" of the last line written, "" before the first
}

// OpenFile opens (creating) the journal in dir and continues its chain from
// the newest file's last complete line. A torn final line (bytes after the
// last newline, from a crash mid-write) is dropped; a complete line is
// never removed, whatever it holds, so Verify can report it.
func OpenFile(dir string, opts FileOptions) (*FileJournal, error) {
	if opts.MaxSize <= 0 {
		opts.MaxSize = defaultMaxSize
	}
	if opts.Keep == 0 {
		opts.Keep = defaultKeep
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	j := &FileJournal{dir: dir, opts: opts, sync: (*os.File).Sync}
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
	if good := bytes.LastIndexByte(data, '\n') + 1; good < len(data) {
		if err := f.Truncate(int64(good)); err != nil {
			f.Close()
			return nil, err
		}
		data = data[:good]
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	j.f, j.name, j.size = f, last, int64(len(data))
	j.day = dayOf(last)
	if len(data) > 0 {
		lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
		j.head = lineHash(lines[len(lines)-1])
	} else {
		// An empty newest file: the chain continues from the file before.
		head, err := lastHashBefore(dir, files, len(files)-1)
		if err != nil {
			f.Close()
			return nil, err
		}
		j.head = head
	}
	return j, nil
}

// lastHashBefore is the hash of the last line of the newest non-empty file
// before files[i], or "".
func lastHashBefore(dir string, files []string, i int) (string, error) {
	for k := i - 1; k >= 0; k-- {
		data, err := os.ReadFile(filepath.Join(dir, files[k]))
		if err != nil {
			return "", err
		}
		data = bytes.TrimSuffix(data[:bytes.LastIndexByte(data, '\n')+1], []byte{'\n'})
		if len(data) > 0 {
			lines := bytes.Split(data, []byte{'\n'})
			return lineHash(lines[len(lines)-1]), nil
		}
	}
	return "", nil
}

// Record appends e, chained to the line before, and returns once it is on
// disk.
func (j *FileJournal) Record(e Entry) (Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e = fill(e, j.opts.Now())
	if err := j.rotate(e.TS); err != nil {
		return e, err
	}
	e.Prev = j.head
	line, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	if _, err := j.f.Write(append(line, '\n')); err != nil {
		return e, err
	}
	if err := j.sync(j.f); err != nil {
		return e, err
	}
	j.size += int64(len(line)) + 1
	j.head = lineHash(line)
	return e, nil
}

// rotate opens a new file when the day has changed, the current file is
// full, or there is none yet. Called with mu held.
func (j *FileJournal) rotate(ts time.Time) error {
	day := ts.UTC().Format("20060102")
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
	f, err := os.OpenFile(filepath.Join(j.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := j.syncDir(); err != nil {
		f.Close()
		return err
	}
	if j.f != nil {
		j.f.Close()
	}
	j.f, j.name, j.day, j.size = f, name, day, 0
	return nil
}

// syncDir makes a new file's directory entry durable.
func (j *FileJournal) syncDir() error {
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

// Head is the hash of the last line written: the anchor a copy elsewhere
// can be compared against.
func (j *FileJournal) Head() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.head
}

// Close closes the current file.
func (j *FileJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

// Prune removes files whose day is older than Keep, never the current one,
// and records each removal as an "audit.prune" entry carrying the removed
// file's last line hash, so Verify knows the chain legitimately begins
// there. It returns the files removed.
func (j *FileJournal) Prune() ([]string, error) {
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
		last, err := lastHashBefore(j.dir, files, i+1)
		if err != nil {
			return removed, err
		}
		// Record first: a crash between the two leaves a record of a
		// removal that did not happen, the safe direction.
		if _, err := j.Record(Entry{User: "system", Source: SourceSystem, Action: "audit.prune",
			Target: name, Result: ResultOK, Detail: map[string]any{"last": last}}); err != nil {
			return removed, err
		}
		if err := os.Remove(filepath.Join(j.dir, name)); err != nil {
			return removed, err
		}
		removed = append(removed, name)
	}
	return removed, nil
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
		if !e.IsDir() && strings.HasPrefix(n, filePrefix) && strings.HasSuffix(n, fileSuffix) && dayOf(n) != "" {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(a, b int) bool { return fileKey(out[a]) < fileKey(out[b]) })
	return out, nil
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
