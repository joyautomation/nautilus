package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Scan reads the journal's files in dir oldest first, one line at a time
// (the whole journal is never held), and calls fn with each entry whose
// time is not before from (zero: all). fn returns false to stop. Entries
// come in file order, which is time order unless the clock stepped back (an
// entry earlier than the current file's day is written to it). A file older
// than from's day is not opened: a file's day is never earlier than the
// entries in it. A line that does not parse (the half-written last
// line of a file being appended to) is skipped.
func Scan(dir string, from time.Time, fn func(Entry) bool) error {
	files, err := listFiles(dir)
	if err != nil {
		return err
	}
	fromDay := ""
	if !from.IsZero() {
		fromDay = from.UTC().Format("20060102")
	}
	for _, name := range files {
		if fromDay != "" && dayOf(name) < fromDay {
			continue
		}
		more, err := scanFile(filepath.Join(dir, name), from, fn)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return nil
}

func scanFile(path string, from time.Time, fn func(Entry) bool) (more bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil // pruned while we read
		}
		return false, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, rerr := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var e Entry
			if json.Unmarshal(line, &e) == nil && (from.IsZero() || !e.TS.Before(from)) {
				if !fn(e) {
					return false, nil
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return true, nil
			}
			return false, rerr
		}
	}
}
