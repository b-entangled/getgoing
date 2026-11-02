package wal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The base contract: what goes in comes back, in order, byte for byte.
func TestAppendReplayRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	want := [][]byte{[]byte("begin"), []byte("put a=1"), []byte("commit")}
	for _, rec := range want {
		if err := l.Append(rec); err != nil {
			t.Fatal(err)
		}
	}

	var got [][]byte
	if err := l.Replay(func(rec []byte) error {
		got = append(got, append([]byte(nil), rec...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("replayed %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if string(got[i]) != string(want[i]) {
			t.Fatalf("record %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A crash mid-write leaves a torn frame at the tail: Open keeps every
// record that landed in full and discards the partial one — exactly
// the guarantee a write-ahead log exists to make.
func TestOpenRecoversTornTailWithoutLosingDurableRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Append([]byte("one"))
	l.Append([]byte("two"))
	l.Close()

	// Simulate a torn append: half a frame lands, then the process dies.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0x40, 0x00, 0x00, 0x00, 0xde, 0xad}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	var got []string
	if err := reopened.Replay(func(rec []byte) error {
		got = append(got, string(rec))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("replayed %v, want [one two]", got)
	}

	// Recovery physically truncated the torn bytes.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	full, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	if err := full.Append([]byte("three")); err != nil {
		t.Fatal(err)
	}
	full.Close()
	var all []string
	final, _ := Open(path)
	final.Replay(func(rec []byte) error {
		all = append(all, string(rec))
		return nil
	})
	final.Close()
	if len(all) != 3 || all[2] != "three" {
		t.Fatalf("after recovery + append: %v, want [one two three]", all)
	}
	_ = st
}

// A flipped bit in the *last* frame's payload is detected by the CRC
// and truncates there: the log replays the valid prefix, never a
// silently corrupted record.
func TestCorruptTailFrameIsDetectedAndTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Append([]byte("alpha"))
	l.Append([]byte("bravo"))
	l.Append([]byte("charlie"))
	l.Close()

	// Flip a byte inside the last payload ("charlie" starts at
	// 2*(8+5)=26, first payload byte at offset 26+8=34).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[34] ^= 0xff
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	var got []string
	if err := reopened.Replay(func(rec []byte) error {
		got = append(got, string(rec))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "alpha" || got[1] != "bravo" {
		t.Fatalf("replayed %v, want [alpha bravo]: corrupt record must never surface", got)
	}
}

// A replay callback can veto: returning an error stops the walk and
// surfaces — the mechanism for "replay until this point, then rebuild".
func TestReplayStopsOnCallbackError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	l, _ := Open(path)
	defer l.Close()
	l.Append([]byte("a"))
	l.Append([]byte("b"))
	l.Append([]byte("c"))

	stop := errors.New("stop here")
	n := 0
	err := l.Replay(func(rec []byte) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Replay = %v, want the callback error", err)
	}
	if n != 2 {
		t.Fatalf("callback ran %d times, want 2", n)
	}
}

// The recovery path is what makes append-after-crash correct: new
// records go after the recovered prefix, not over it.
func TestAppendAfterRecoveryContinuesAfterThePrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	l, _ := Open(path)
	l.Append([]byte("first"))
	l.Append([]byte("second"))
	l.Close()

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte{1, 2, 3}) // torn junk
	f.Close()

	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Append([]byte("third")); err != nil {
		t.Fatal(err)
	}
	recovered.Close()

	final, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	var got []string
	final.Replay(func(rec []byte) error {
		got = append(got, string(rec))
		return nil
	})
	if len(got) != 3 || got[0] != "first" || got[1] != "second" || got[2] != "third" {
		t.Fatalf("replayed %v, want [first second third]", got)
	}
}

// Oversized frames are rejected at append time, before they can
// poison the file with an unscannable length.
func TestAppendRejectsOversizedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := l.Append(make([]byte, maxRecord+1)); err == nil {
		t.Fatal("oversized record accepted")
	}
}
