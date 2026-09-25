package logstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRingWrapKeepsNewest(t *testing.T) {
	r := NewRing(5)
	for i := 0; i < 12; i++ {
		r.Add(time.Time{}, LevelInfo, fmt.Sprintf("line %d", i))
	}
	if r.Len() != 5 {
		t.Fatalf("Len = %d, want 5", r.Len())
	}
	got := r.Tail(100)
	if len(got) != 5 {
		t.Fatalf("Tail(100) = %d entries, want 5", len(got))
	}
	for i, e := range got {
		if want := fmt.Sprintf("line %d", 7+i); e.Line != want {
			t.Errorf("entry %d = %q, want %q (oldest first)", i, e.Line, want)
		}
	}
	if tail := r.Tail(2); len(tail) != 2 || tail[0].Line != "line 10" || tail[1].Line != "line 11" {
		t.Errorf("Tail(2) = %+v, want lines 10 and 11", tail)
	}

	after := r.After(got[2].ID, 0)
	if len(after) != 2 || after[0].Line != "line 10" {
		t.Errorf("After(id of line 9) = %+v, want lines 10, 11", after)
	}
	if capped := r.After(0, 3); len(capped) != 3 || capped[0].Line != "line 7" {
		t.Errorf("After(0, limit 3) = %+v, want the OLDEST three (7,8,9), so a slow reader never skips lines", capped)
	}
	if none := r.After(got[4].ID, 0); len(none) != 0 {
		t.Errorf("After(newest) = %+v, want nothing", none)
	}
}

func TestRingIDsAreStrictlyIncreasing(t *testing.T) {
	r := NewRing(10)
	same := time.Date(2026, 9, 25, 4, 0, 0, 123000, time.UTC)
	a := r.Add(same, LevelInfo, "a")
	b := r.Add(same, LevelInfo, "b")
	c := r.Add(same.Add(-time.Hour), LevelInfo, "c") // the clock stepped backwards
	if !(a.ID < b.ID && b.ID < c.ID) {
		t.Fatalf("ids not strictly increasing: %d %d %d", a.ID, b.ID, c.ID)
	}
	if a.ID != same.UnixMicro() {
		t.Errorf("first id = %d, want the timestamp in microseconds (%d)", a.ID, same.UnixMicro())
	}
	if a.TS != "2026-09-25T04:00:00.000123Z" {
		t.Errorf("ts = %q, want RFC3339Nano UTC", a.TS)
	}
}

func TestRingTruncatesHugeLines(t *testing.T) {
	r := NewRing(2)
	e := r.Add(time.Time{}, LevelInfo, strings.Repeat("x", MaxLineBytes*2))
	if len(e.Line) > MaxLineBytes+3 {
		t.Errorf("stored line is %d bytes, want at most %d", len(e.Line), MaxLineBytes+3)
	}
}

func TestLevelHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"debug": LevelDebug, "INFO": LevelInfo, "warn": LevelWarn, "Warning": LevelWarn, "error": LevelError, "weird": LevelInfo, "": LevelInfo,
	} {
		if got := NormalizeLevel(in); got != want {
			t.Errorf("NormalizeLevel(%q) = %q, want %q", in, got, want)
		}
	}
	if _, ok := LevelRank("nope"); ok {
		t.Error("LevelRank accepted an unknown level")
	}
	warn, _ := LevelRank("warn")
	errRank, _ := LevelRank("error")
	e := Entry{Level: LevelWarn, Line: "Disk FULL on /var"}
	if !e.Matches(warn, "") || e.Matches(errRank, "") {
		t.Error("Matches level threshold wrong")
	}
	if !e.Matches(0, "disk full") || e.Matches(0, "nope") {
		t.Error("Matches substring is not case-insensitive / not filtering")
	}
	for l, want := range map[slog.Level]string{slog.LevelDebug: "debug", slog.LevelInfo: "info", slog.LevelWarn: "warn", slog.LevelError: "error"} {
		if got := FromSlogLevel(l); got != want {
			t.Errorf("FromSlogLevel(%v) = %q, want %q", l, got, want)
		}
	}
}

func TestEncodeDecodeShape(t *testing.T) {
	e := Entry{ID: 1790304326774872, TS: "2026-09-25T04:00:00.123Z", Level: "error", Line: `{"msg":"x"}`}
	var m map[string]any
	if err := json.Unmarshal([]byte(e.Encode()), &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 4 || m["level"] != "error" || m["line"] != `{"msg":"x"}` || m["ts"] != e.TS {
		t.Errorf("encoded element = %v, want exactly id/ts/level/line", m)
	}
	back, ok := Decode(e.Encode())
	if !ok || back != e {
		t.Errorf("Decode(Encode) = %+v %v, want the same entry", back, ok)
	}
	if _, ok := Decode("not json"); ok {
		t.Error("Decode accepted garbage")
	}
	if _, ok := Decode(`{"id":0}`); ok {
		t.Error("Decode accepted an entry without an id")
	}
}

func TestHandlerKeepsOutputAndFillsRing(t *testing.T) {
	var out bytes.Buffer
	ring := NewRing(10)
	logger := slog.New(NewHandler(&out, nil, ring))

	logger.Info("hello", "k", "v")
	logger.With("component", "x").WithGroup("g").Warn("careful", "n", 3)
	logger.Debug("dropped by the default level")
	logger.Error("boom")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("destination got %d lines, want 3 (debug is below the handler's level):\n%s", len(lines), out.String())
	}
	entries := ring.Tail(10)
	if len(entries) != 3 {
		t.Fatalf("ring has %d entries, want 3", len(entries))
	}
	for i, e := range entries {
		if e.Line != lines[i] {
			t.Errorf("entry %d line = %q, want the exact destination line %q", i, e.Line, lines[i])
		}
	}
	for i, want := range []string{"info", "warn", "error"} {
		if entries[i].Level != want {
			t.Errorf("entry %d level = %q, want %q", i, entries[i].Level, want)
		}
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(entries[1].Line), &second); err != nil {
		t.Fatalf("stored line is not JSON: %v", err)
	}
	if second["component"] != "x" || second["msg"] != "careful" {
		t.Errorf("derived handler lost its attrs: %v", second)
	}
	if g, _ := second["g"].(map[string]any); g == nil || g["n"] != float64(3) {
		t.Errorf("derived handler lost its group: %v", second)
	}
	// The entry id is the record's own time in microseconds (bumped only when
	// two lines share a microsecond).
	recTime, _ := second["time"].(string)
	parsed, err := time.Parse(time.RFC3339Nano, recTime)
	if err != nil {
		t.Fatal(err)
	}
	if entries[1].ID < parsed.UnixMicro() || entries[1].ID > parsed.UnixMicro()+2 {
		t.Errorf("entry id = %d, want the record time in microseconds (%d)", entries[1].ID, parsed.UnixMicro())
	}
}

func TestHandlerConcurrentLevelsStayWithTheirLines(t *testing.T) {
	ring := NewRing(4000)
	logger := slog.New(NewHandler(&bytes.Buffer{}, nil, ring))
	var wg sync.WaitGroup
	levels := []struct {
		name string
		log  func(string)
	}{
		{"info", func(m string) { logger.Info(m) }},
		{"warn", func(m string) { logger.Warn(m) }},
		{"error", func(m string) { logger.Error(m) }},
	}
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				l := levels[(g+i)%len(levels)]
				l.log(l.name)
			}
		}(g)
	}
	wg.Wait()

	entries := ring.Tail(4000)
	if len(entries) != 2400 {
		t.Fatalf("ring has %d entries, want 2400", len(entries))
	}
	var prev int64
	for _, e := range entries {
		if e.ID <= prev {
			t.Fatalf("ids not strictly increasing: %d after %d", e.ID, prev)
		}
		prev = e.ID
		var m struct{ Level, Msg string }
		if err := json.Unmarshal([]byte(e.Line), &m); err != nil {
			t.Fatalf("line is not JSON: %v", err)
		}
		if strings.ToLower(m.Level) != e.Level || m.Msg != e.Level {
			t.Fatalf("entry level %q does not match its own line %q", e.Level, e.Line)
		}
	}
}

// countingCmdable fails the test if a publish touches Redis when it should not.
type countingCmdable struct {
	redis.Cmdable
	pipelines int
}

func (c *countingCmdable) Pipeline() redis.Pipeliner {
	c.pipelines++
	return c.Cmdable.Pipeline()
}

func TestPublisherSkipsWhenNothingIsNew(t *testing.T) {
	c := &countingCmdable{} // a nil Cmdable: any real use would panic
	p := NewPublisher(c, "unit", NewRing(10), nil)
	if err := p.Flush(context.Background()); err != nil {
		t.Fatalf("Flush on an empty ring: %v", err)
	}
	if c.pipelines != 0 {
		t.Errorf("an empty ring opened %d pipelines, want 0", c.pipelines)
	}
}

func TestPublisherDropsInsteadOfGrowingWhenRedisIsDown(t *testing.T) {
	// Nothing listens on port 1: every dial fails immediately.
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	defer rdb.Close()
	ring := NewRing(50)
	p := NewPublisher(rdb, "unit-down", ring, nil)
	for i := 0; i < 500; i++ {
		ring.Add(time.Time{}, LevelInfo, fmt.Sprintf("line %d", i))
	}
	if err := p.Flush(context.Background()); err == nil {
		t.Fatal("Flush against a dead Redis succeeded")
	}
	if ring.Len() != 50 {
		t.Errorf("ring holds %d entries, want it capped at 50", ring.Len())
	}
	if p.last != 0 {
		t.Errorf("cursor moved to %d after a failed publish, want it left at 0 so the entries are retried", p.last)
	}
}

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set, skipping test against a real Redis instance")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func TestPublisherPushesNewEntriesAndTrimsToNewest(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	source := fmt.Sprintf("unit-%d", time.Now().UnixNano())
	key := Key(source)
	t.Cleanup(func() { rdb.Del(ctx, key) })

	// A ring bigger than the list cap, so the trim has something to cut.
	ring := NewRing(RingSize + 500)
	p := NewPublisher(rdb, source, ring, nil)
	for i := 0; i < RingSize+300; i++ {
		ring.Add(time.Time{}, LevelInfo, fmt.Sprintf("bulk %d", i))
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n := rdb.LLen(ctx, key).Val(); n != RingSize {
		t.Fatalf("list length after a 2300-entry publish = %d, want the cap %d", n, RingSize)
	}
	first, _ := Decode(rdb.LIndex(ctx, key, 0).Val())
	if first.Line != "bulk 300" {
		t.Errorf("oldest kept entry = %q, want %q (the newest 2000 survive)", first.Line, "bulk 300")
	}

	// Nothing new: the list is untouched.
	if err := p.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if n := rdb.LLen(ctx, key).Val(); n != RingSize {
		t.Errorf("list length changed to %d on a publish with nothing new", n)
	}

	// New entries are appended after the old ones, in order, once each.
	for i := 0; i < 5; i++ {
		ring.Add(time.Time{}, LevelWarn, fmt.Sprintf("fresh %d", i))
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatalf("third Flush: %v", err)
	}
	tail := rdb.LRange(ctx, key, -5, -1).Val()
	var prev int64
	for i, raw := range tail {
		e, ok := Decode(raw)
		if !ok || e.Line != fmt.Sprintf("fresh %d", i) || e.Level != LevelWarn {
			t.Fatalf("tail element %d = %s, want fresh %d as a warn", i, raw, i)
		}
		if e.ID <= prev {
			t.Fatalf("ids not increasing in the list: %d after %d", e.ID, prev)
		}
		prev = e.ID
	}
	if n := rdb.LLen(ctx, key).Val(); n != RingSize {
		t.Errorf("list length = %d, want it held at %d", n, RingSize)
	}
}

func TestPublisherRunMirrorsALoggerIntoRedisEverySecond(t *testing.T) {
	rdb := testRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	source := fmt.Sprintf("unit-run-%d", time.Now().UnixNano())
	key := Key(source)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })

	ring := NewRing(RingSize)
	logger := slog.New(NewHandler(&bytes.Buffer{}, nil, ring))
	done := make(chan struct{})
	go func() {
		NewPublisher(rdb, source, ring, logger).Run(ctx)
		close(done)
	}()

	logger.Warn("first line", "k", 1)
	waitFor := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if rdb.LLen(context.Background(), key).Val() >= want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("list %s never reached %d entries (has %d)", key, want, rdb.LLen(context.Background(), key).Val())
	}
	waitFor(1)
	logger.Error("second line")
	waitFor(2)

	cancel()
	<-done
	elems := rdb.LRange(context.Background(), key, 0, -1).Val()
	if len(elems) != 2 {
		t.Fatalf("list has %d elements, want 2 (each line published once): %v", len(elems), elems)
	}
	first, _ := Decode(elems[0])
	second, _ := Decode(elems[1])
	if first.Level != LevelWarn || second.Level != LevelError || first.ID >= second.ID {
		t.Errorf("published %+v then %+v, want the warn line then the error line in id order", first, second)
	}
	if !strings.Contains(first.Line, `"msg":"first line"`) {
		t.Errorf("published line = %q, want the original JSON log line", first.Line)
	}
}
