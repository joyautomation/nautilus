package oplog

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// maxLine is the longest line the log file may hold, its newline
// included: well over maxEntryBytes, so an entry once stored is read back.
const maxLine = 1 << 20

// scanLines reads a log file line by line, holding one line at a time
// (#1455: never the whole file). fn gets each whole line without its
// newline and says whether it is good; an error from fn stops the scan and
// is returned.
//
// The scan stops at the first line that is not good: one fn refuses, one
// longer than maxLine, or a last line with no newline. A line is whole only
// with its newline: each goes out in one write, newline last, synced
// before it is acknowledged, so a line cut before its newline was never
// acknowledged, whatever it holds.
//
// good is how many bytes of good lines precede the line that stopped it,
// size how many the reader held in all. damaged: something other than
// blank space follows that line, so it was not a torn final write but
// damage (see open).
func scanLines(r io.Reader, fn func(line []byte) (bool, error)) (good, size int64, damaged bool, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var long []byte
	for {
		line, n, tooLong, rerr := nextLine(br, &long)
		size += n
		switch {
		case rerr != nil && !errors.Is(rerr, io.EOF):
			return good, size, false, rerr
		case n == 0:
			return good, size, false, nil // the end
		case errors.Is(rerr, io.EOF):
			// A last line with no newline (nextLine ends at one): torn.
			return good, size, false, nil
		case tooLong:
			d, k, e := restDamaged(br)
			return good, size + k, d, e
		}
		ok, ferr := fn(line[:len(line)-1])
		if ferr != nil {
			return good, size, false, ferr
		}
		if !ok {
			d, k, e := restDamaged(br)
			return good, size + k, d, e
		}
		good += n
	}
}

// nextLine reads one line, its newline included (none at the end of the
// file: then err is io.EOF). n is how many bytes it read. A line longer
// than maxLine is read to its end but not kept: tooLong, line nil. long is
// the caller's scratch for a line longer than the reader's buffer.
func nextLine(br *bufio.Reader, long *[]byte) (line []byte, n int64, tooLong bool, err error) {
	*long = (*long)[:0]
	for {
		chunk, err := br.ReadSlice('\n')
		n += int64(len(chunk))
		switch {
		case tooLong:
		case n > maxLine:
			tooLong, *long = true, (*long)[:0]
		case errors.Is(err, bufio.ErrBufferFull) || len(*long) > 0:
			*long = append(*long, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		switch {
		case tooLong:
			return nil, n, true, err
		case len(*long) > 0:
			return *long, n, false, err
		}
		return chunk, n, false, err
	}
}

// restDamaged reads what follows a bad line: damaged when any of it is
// not blank space. n is how many bytes it read.
func restDamaged(br *bufio.Reader) (damaged bool, n int64, err error) {
	buf := make([]byte, 32<<10)
	for {
		k, rerr := br.Read(buf)
		n += int64(k)
		if len(bytes.TrimSpace(buf[:k])) > 0 {
			damaged = true
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				rerr = nil
			}
			return damaged, n, rerr
		}
	}
}
