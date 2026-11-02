// Package wal implements a write-ahead log: an append-only sequence of
// checksummed records that survives process crashes. It is the
// durability primitive underneath most system designs — databases
// commit to a log before touching pages, queues persist messages
// before acknowledging, replicas ship their log to catch followers up.
//
// Format (little-endian): [4B payload length][4B CRC-32 of payload][payload]
//
// Design decisions made explicit here:
//
//   - One Write per record. A crash can tear that write (some bytes
//     land, some do not) — the log is designed around that fact
//     rather than pretending it away.
//   - Open recovers the longest valid prefix: it scans frames, and
//     truncates at the first frame with a short header, short body or
//     bad CRC. Whatever was durably written in full replays; whatever
//     was torn disappears. The trade-off: corruption in the *middle*
//     of a log is indistinguishable from a torn tail and truncates
//     there too — real systems split logs into segments so a bad
//     segment is a bounded, visible event.
//   - Durability is explicit: Append returns after the bytes reach the
//     OS, not the disk. Call Sync when a group of records must
//     survive a power loss (batching syncs is the classic group-commit
//     trade-off between latency and safety).
package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"
)

const (
	headerSize = 8
	maxRecord  = 16 << 20 // sanity bound: larger lengths are garbage
)

// ErrCorrupt reports a structural problem that recovery could not
// resolve (e.g. a record larger than maxRecord during a scan).
var ErrCorrupt = errors.New("wal: corrupt log")

// Log is an open write-ahead log. It is safe for concurrent use:
// appends and replays serialise on an internal mutex.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// Open opens or creates the log at path, recovers the longest valid
// record prefix (truncating any torn tail), and positions the file for
// appends.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: open: %w", err)
	}
	good, err := scan(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Truncate(good); err != nil {
		f.Close()
		return nil, fmt.Errorf("wal: truncate torn tail: %w", err)
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		f.Close()
		return nil, fmt.Errorf("wal: seek: %w", err)
	}
	return &Log{f: f, path: path}, nil
}

// Append adds one record. The whole frame goes down in a single Write;
// if that write tears, the next Open truncates it away.
func (l *Log) Append(record []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(record) > maxRecord {
		return fmt.Errorf("wal: record of %d bytes exceeds max %d", len(record), maxRecord)
	}
	frame := make([]byte, headerSize+len(record))
	binary.LittleEndian.PutUint32(frame[0:4], uint32(len(record)))
	binary.LittleEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(record))
	copy(frame[headerSize:], record)

	if _, err := l.f.Write(frame); err != nil {
		return fmt.Errorf("wal: append: %w", err)
	}
	return nil
}

// Sync forces appended records through the OS to disk.
func (l *Log) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("wal: sync: %w", err)
	}
	return nil
}

// Replay reads every record in order and passes it to fn. Returning a
// non-nil error from fn stops the replay and is passed through. Replay
// only ever sees frames Open already validated.
func (l *Log) Replay(fn func(record []byte) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("wal: replay seek: %w", err)
	}
	for {
		record, err := readFrame(l.f)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wal: replay: %w", err)
		}
		if err := fn(record); err != nil {
			return err
		}
	}
}

// Close syncs and closes the log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.f.Sync(); err != nil {
		l.f.Close()
		return err
	}
	return l.f.Close()
}

// Path returns the file this log lives in.
func (l *Log) Path() string { return l.path }

// scan walks frames from the start and returns the offset just past
// the last fully valid one. It stops at the first torn or corrupt
// frame — that boundary is where recovery truncates.
func scan(f *os.File) (int64, error) {
	var offset int64
	for {
		record, n, err := readFrameChecked(f)
		if err == io.EOF {
			return offset, nil
		}
		if err == ErrCorrupt {
			return offset, nil // torn/garbage tail: truncate here
		}
		if err != nil {
			return 0, err
		}
		_ = record
		offset += n
	}
}

// readFrame reads a frame from the current position with no
// validation — the replay path (post-recovery).
func readFrame(f *os.File) ([]byte, error) {
	record, _, err := readFrameChecked(f)
	return record, err
}

// readFrameChecked reads one frame, distinguishing clean EOF from a
// torn or corrupt frame.
func readFrameChecked(f *os.File) ([]byte, int64, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(f, header); err != nil {
		if err == io.EOF {
			return nil, 0, io.EOF
		}
		return nil, 0, ErrCorrupt // torn header
	}
	length := binary.LittleEndian.Uint32(header[0:4])
	wantCRC := binary.LittleEndian.Uint32(header[4:8])
	if length > maxRecord {
		return nil, 0, ErrCorrupt // garbage length
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(f, payload); err != nil {
		return nil, 0, ErrCorrupt // torn body
	}
	if crc32.ChecksumIEEE(payload) != wantCRC {
		return nil, 0, ErrCorrupt // corruption
	}
	return payload, headerSize + int64(length), nil
}
