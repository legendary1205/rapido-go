package nodelog

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedRing(capacity int, start time.Time, step time.Duration) *Ring {
	r := NewRing(capacity)
	now := start
	r.now = func() time.Time {
		t := now
		now = now.Add(step)
		return t
	}
	return r
}

func TestRingAddSinceNewest(t *testing.T) {
	r := fixedRing(5, time.UnixMicro(1_000_000), time.Millisecond)
	if r.Len() != 0 || r.OldestID() != 0 || r.NewestID() != 0 || len(r.Since(0, 10)) != 0 || len(r.Newest(10)) != 0 {
		t.Fatal("an empty ring reports content")
	}

	var ids []int64
	for i := 1; i <= 3; i++ {
		ids = append(ids, r.Add(LevelInfo, fmt.Sprintf("line %d", i)))
	}
	if r.Len() != 3 || r.OldestID() != ids[0] || r.NewestID() != ids[2] {
		t.Fatalf("len %d oldest %d newest %d", r.Len(), r.OldestID(), r.NewestID())
	}
	if got := r.Since(0, 100); len(got) != 3 || got[0].Text != "line 1" || got[2].Text != "line 3" {
		t.Errorf("Since(0) = %+v", got)
	}
	if got := r.Since(ids[0], 100); len(got) != 2 || got[0].Text != "line 2" {
		t.Errorf("Since(first) = %+v", got)
	}
	if got := r.Since(ids[2], 100); len(got) != 0 {
		t.Errorf("Since(newest) = %+v, want nothing", got)
	}
	if got := r.Since(ids[0], 1); len(got) != 1 || got[0].Text != "line 2" {
		t.Errorf("Since with a limit = %+v, want the OLDEST matching entry so a caller can walk forward", got)
	}
	if got := r.Newest(2); len(got) != 2 || got[0].Text != "line 2" || got[1].Text != "line 3" {
		t.Errorf("Newest(2) = %+v", got)
	}
	if got := r.Newest(99); len(got) != 3 {
		t.Errorf("Newest(99) = %d entries", len(got))
	}
}

func TestRingWrapsAndKeepsTheNewest(t *testing.T) {
	r := fixedRing(4, time.UnixMicro(5_000_000), time.Millisecond)
	var ids []int64
	for i := 1; i <= 10; i++ {
		ids = append(ids, r.Add(LevelWarn, fmt.Sprintf("n%d", i)))
	}
	if r.Len() != 4 {
		t.Fatalf("len = %d, want 4", r.Len())
	}
	got := r.Since(0, 100)
	if len(got) != 4 || got[0].Text != "n7" || got[3].Text != "n10" {
		t.Fatalf("after wrapping the ring holds %+v, want n7..n10", got)
	}
	if r.OldestID() != ids[6] || r.NewestID() != ids[9] {
		t.Errorf("oldest %d newest %d, want %d %d", r.OldestID(), r.NewestID(), ids[6], ids[9])
	}
	// A cursor older than anything held gets everything held.
	if got := r.Since(ids[1], 100); len(got) != 4 || got[0].Text != "n7" {
		t.Errorf("Since(a cursor that rolled off) = %+v", got)
	}
	// A cursor in the middle resumes exactly after it.
	if got := r.Since(ids[7], 100); len(got) != 2 || got[0].Text != "n9" {
		t.Errorf("Since(mid) = %+v", got)
	}
	// Walking forward with the last id received visits every entry once.
	var walked []string
	cursor := int64(0)
	for {
		batch := r.Since(cursor, 3)
		if len(batch) == 0 {
			break
		}
		for _, e := range batch {
			walked = append(walked, e.Text)
			cursor = e.ID
		}
	}
	if strings.Join(walked, ",") != "n7,n8,n9,n10" {
		t.Errorf("walk = %v", walked)
	}
}

// Ids never repeat or go back, even when many lines share a microsecond or the
// clock steps backwards.
func TestRingIDsAreStrictlyMonotonic(t *testing.T) {
	r := NewRing(50)
	times := []time.Time{time.UnixMicro(1000), time.UnixMicro(1000), time.UnixMicro(1000), time.UnixMicro(500), time.UnixMicro(2000), time.UnixMicro(1)}
	i := 0
	r.now = func() time.Time { t := times[i%len(times)]; i++; return t }
	var prev int64
	for range 12 {
		id := r.Add(LevelInfo, "x")
		if id <= prev {
			t.Fatalf("id %d after %d", id, prev)
		}
		prev = id
	}
	// Once the clock is ahead again the ids follow it.
	r.now = func() time.Time { return time.UnixMicro(prev + 1_000_000) }
	if id := r.Add(LevelInfo, "y"); id != prev+1_000_000 {
		t.Errorf("id = %d, want the clock value %d", id, prev+1_000_000)
	}
}

func TestRingDefaultsAndTruncation(t *testing.T) {
	if r := NewRing(0); len(r.buf) != DefaultRingSize || DefaultRingSize != 5000 {
		t.Errorf("default size = %d", len(r.buf))
	}
	r := NewRing(3)
	r.Add(LevelInfo, strings.Repeat("é", maxLineBytes)) // 2 bytes per rune: cut mid-rune
	e := r.Newest(1)[0]
	if len(e.Text) > maxLineBytes+len("...(truncated)") || !strings.HasSuffix(e.Text, "...(truncated)") {
		t.Errorf("long line not bounded: %d bytes", len(e.Text))
	}
	if strings.ToValidUTF8(e.Text, "") != e.Text {
		t.Error("truncation split a multi-byte character")
	}
	if e.Time().IsZero() || e.Time().Location() != time.UTC {
		t.Errorf("Time() = %v", e.Time())
	}
}

func TestRingConcurrent(t *testing.T) {
	r := NewRing(100)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				r.Add(LevelInfo, fmt.Sprintf("g%d-%d", g, i))
				if i%50 == 0 {
					r.Since(0, 10)
					r.Newest(5)
					r.OldestID()
				}
			}
		}()
	}
	wg.Wait()
	if r.Len() != 100 {
		t.Errorf("len = %d", r.Len())
	}
	all := r.Since(0, 1000)
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("ids out of order at %d: %d then %d", i, all[i-1].ID, all[i].ID)
		}
	}
}

func TestTeeJSONRecordsLevelsAndPassesThrough(t *testing.T) {
	r := NewRing(10)
	var dst bytes.Buffer
	w := r.TeeJSON(&dst)

	lines := []string{
		`{"time":"2026-09-25T04:00:00.1Z","level":"INFO","msg":"listening","addr":":1"}`,
		`{"time":"2026-09-25T04:00:00.2Z","level":"WARN","msg":"push report: request failed"}`,
		`{"time":"2026-09-25T04:00:00.3Z","level":"ERROR","msg":"pull config: start node","error":"x"}`,
		`{"time":"2026-09-25T04:00:00.4Z","level":"DEBUG","msg":"d"}`,
		`{"time":"2026-09-25T04:00:00.5Z","msg":"no level here"}`,
	}
	for _, l := range lines {
		if n, err := w.Write([]byte(l + "\n")); err != nil || n != len(l)+1 {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if dst.String() != strings.Join(lines, "\n")+"\n" {
		t.Errorf("the destination did not receive the lines untouched: %q", dst.String())
	}
	got := r.Since(0, 100)
	wantLevels := []string{LevelInfo, LevelWarn, LevelError, LevelDebug, LevelInfo}
	if len(got) != len(lines) {
		t.Fatalf("recorded %d lines, want %d", len(got), len(lines))
	}
	for i, e := range got {
		if e.Level != wantLevels[i] || e.Text != lines[i] {
			t.Errorf("entry %d = (%s) %s, want (%s) %s", i, e.Level, e.Text, wantLevels[i], lines[i])
		}
	}

	// One write holding several lines becomes several entries, blank lines vanish.
	r2 := NewRing(10)
	r2.TeeJSON(&dst).Write([]byte(lines[0] + "\n\n" + lines[2] + "\r\n"))
	if got := r2.Since(0, 10); len(got) != 2 || got[1].Level != LevelError {
		t.Errorf("multi-line write recorded %+v", got)
	}
}
