package nodelog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer that is safe to read while a goroutine writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const sampleStream = "\x1b[36mINFO\x1b[0m[0000] inbound/vless[main#20001]: tcp server started at 0.0.0.0:20001\n" +
	"\x1b[31mERROR\x1b[0m[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: EOF\n" +
	"\x1b[31mERROR\x1b[0m[0001] inbound/vless[main#20001]: process connection from 1.2.3.5:1: EOF\n" +
	"\x1b[31mERROR\x1b[0m[0002] [1 1s] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: mux connection closed: read frame header: EOF\n" +
	"\x1b[31mERROR\x1b[0m[0003] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: TLS handshake: EOF\n" +
	"\x1b[31mERROR\x1b[0m[0004] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: unknown UUID: 0c1ec6e8\n" +
	"\x1b[31mERROR\x1b[0m[0005] connection: open connection to 5.6.7.8:443 using outbound/direct[germany~wg]: dial tcp 5.6.7.8:443: i/o timeout\n" +
	"\x1b[31mERROR\x1b[0m[0005] connection: open connection to 5.6.7.9:443 using outbound/direct[germany~wg]: dial tcp 5.6.7.9:443: i/o timeout\n" +
	"\x1b[31mERROR\x1b[0m[0006] inbound/vless[main#20001]: transport serve error: too many open files\n" +
	"\x1b[33mWARN\x1b[0m[0007] router: something odd\n" +
	"a continuation line of the warning\n"

func TestCaptureDropsNoiseKeepsTheRest(t *testing.T) {
	ring := NewRing(100)
	agg := NewAggregator()
	var journal syncBuffer
	c := NewCapture(ring, agg, &journal)

	c.Process(strings.NewReader(sampleStream))

	totals := agg.Totals()
	want := map[string]int64{
		KindClientEOF:                   2,
		KindMuxClosed:                   1,
		KindTLSHandshake:                1,
		KindUnknownUUID:                 1,
		KindDialTimeout + ":germany~wg": 2,
	}
	if len(totals) != len(want) {
		t.Errorf("totals = %v, want %v", totals, want)
	}
	for k, v := range want {
		if totals[k] != v {
			t.Errorf("total[%s] = %d, want %d", k, totals[k], v)
		}
	}

	// The journal gets exactly the kept lines, byte for byte as they arrived
	// (colour codes and all - they were not this package's to change).
	keptLines := []string{
		"\x1b[36mINFO\x1b[0m[0000] inbound/vless[main#20001]: tcp server started at 0.0.0.0:20001",
		"\x1b[31mERROR\x1b[0m[0006] inbound/vless[main#20001]: transport serve error: too many open files",
		"\x1b[33mWARN\x1b[0m[0007] router: something odd",
		"a continuation line of the warning",
	}
	if got := journal.String(); got != strings.Join(keptLines, "\n")+"\n" {
		t.Errorf("journal =\n%q\nwant\n%q", got, strings.Join(keptLines, "\n")+"\n")
	}

	// The ring holds the same lines, stripped of colour and of the level prefix,
	// with the level as a field; a wrapped line inherits the level above it.
	entries := ring.Since(0, 100)
	wantRing := []struct{ level, text string }{
		{LevelInfo, "inbound/vless[main#20001]: tcp server started at 0.0.0.0:20001"},
		{LevelError, "inbound/vless[main#20001]: transport serve error: too many open files"},
		{LevelWarn, "router: something odd"},
		{LevelWarn, "a continuation line of the warning"},
	}
	if len(entries) != len(wantRing) {
		t.Fatalf("ring = %+v", entries)
	}
	for i, w := range wantRing {
		if entries[i].Level != w.level || entries[i].Text != w.text {
			t.Errorf("ring[%d] = (%s) %q, want (%s) %q", i, entries[i].Level, entries[i].Text, w.level, w.text)
		}
		if strings.Contains(entries[i].Text, "\x1b") {
			t.Errorf("ring[%d] carries an escape code", i)
		}
	}
}

func TestCaptureHandlesUnterminatedAndHugeLines(t *testing.T) {
	ring := NewRing(10)
	var journal syncBuffer
	c := NewCapture(ring, NewAggregator(), &journal)

	huge := "ERROR[0001] " + strings.Repeat("x", 200<<10)
	c.Process(strings.NewReader("INFO[0000] first\n" + huge + "\nERROR[0002] last line without a newline"))

	entries := ring.Since(0, 10)
	if len(entries) != 3 {
		t.Fatalf("ring holds %d entries, want 3", len(entries))
	}
	if entries[0].Text != "first" || entries[2].Text != "last line without a newline" || entries[2].Level != LevelError {
		t.Errorf("entries = %.60s / %.60s", entries[0].Text, entries[2].Text)
	}
	if n := len(entries[1].Text); n == 0 || n > maxLineBytes+len("...(truncated)") {
		t.Errorf("the huge line stored as %d bytes", n)
	}
}

func TestAggregatorFlushLogsOneInfoLine(t *testing.T) {
	agg := NewAggregator()
	base := time.Date(2026, 9, 25, 4, 0, 0, 0, time.UTC)
	agg.since = base
	clock := base
	agg.now = func() time.Time { return clock }

	var out syncBuffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))

	clock = base.Add(30 * time.Second)
	agg.Flush(logger)
	if out.String() != "" {
		t.Fatalf("an empty window logged %q", out.String())
	}

	for range 5787 {
		agg.Count(KindClientEOF)
	}
	for range 291 {
		agg.Count(KindTLSHandshake)
	}
	for range 65 {
		agg.Count(KindDialTimeout + ":germany~wg")
	}
	clock = base.Add(90 * time.Second) // the window started at the empty flush, 30s in
	agg.Flush(logger)

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("flush wrote %d lines: %s", len(lines), out.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["msg"] != "suppressed benign client errors" || rec["level"] != "INFO" {
		t.Errorf("record = %v", rec)
	}
	if rec["client_eof"] != float64(5787) || rec["tls_handshake"] != float64(291) || rec["dial_timeout:germany~wg"] != float64(65) {
		t.Errorf("counts missing from %v", rec)
	}
	if rec["total"] != float64(5787+291+65) || rec["window_seconds"] != float64(60) {
		t.Errorf("total/window in %v", rec)
	}

	// The window resets; the cumulative counters do not.
	out.b.Reset()
	agg.Flush(logger)
	if out.String() != "" {
		t.Errorf("the second flush repeated the counts: %s", out.String())
	}
	if agg.Totals()[KindClientEOF] != 5787 {
		t.Errorf("cumulative total lost on flush: %v", agg.Totals())
	}
	agg.Count(KindClientEOF)
	agg.Flush(logger)
	if !strings.Contains(out.String(), `"total":1`) || !strings.Contains(out.String(), `"client_eof":1`) {
		t.Errorf("third window = %s", out.String())
	}
	if agg.Totals()[KindClientEOF] != 5788 {
		t.Errorf("cumulative = %v", agg.Totals())
	}

	// Totals is a copy.
	agg.Totals()[KindClientEOF] = 0
	if agg.Totals()[KindClientEOF] != 5788 {
		t.Error("Totals leaked the internal map")
	}
}

func TestAggregatorRunFlushesOnTickAndOnShutdown(t *testing.T) {
	agg := NewAggregator()
	var out syncBuffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { agg.Run(ctx, logger, 20*time.Millisecond); close(done) }()

	agg.Count(KindClientReset)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), KindClientReset) {
		if time.Now().After(deadline) {
			t.Fatal("no flush on the ticker")
		}
		time.Sleep(5 * time.Millisecond)
	}

	agg.Count(KindBrokenPipe)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	if !strings.Contains(out.String(), KindBrokenPipe) {
		t.Errorf("the last partial window was lost at shutdown: %s", out.String())
	}
}

// The whole path on a real pipe: os.Stderr is replaced, lines written there by
// any component are filtered, and Close restores everything.
func TestInterceptStderrEndToEnd(t *testing.T) {
	realStderr := os.Stderr
	// Point the "real" stderr at a temp file so we can read what reached it.
	journal, err := os.CreateTemp(t.TempDir(), "journal")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	os.Stderr = journal
	defer func() { os.Stderr = realStderr }()

	ring := NewRing(1000)
	agg := NewAggregator()
	c, err := InterceptStderr(ring, agg)
	if err != nil {
		t.Fatal(err)
	}
	if os.Stderr == journal {
		t.Fatal("os.Stderr was not replaced")
	}

	// What sing-box does: capture os.Stderr when a core is built, write to it from
	// many goroutines. Two "cores" in a row (a restart) share the one pipe.
	for core := 0; core < 2; core++ {
		w := io.Writer(os.Stderr)
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					fmt.Fprintf(w, "ERROR[0001] inbound/vless[m#1]: process connection from 1.2.3.4:%d: EOF\n", i)
					fmt.Fprintf(w, "INFO[0001] [%d %d] kept line core %d goroutine %d\n", core, g, core, g)
				}
			}()
		}
		wg.Wait()
	}

	stale := os.Stderr // what a core that is still shutting down would hold
	c.Close()
	c.Close() // idempotent
	if os.Stderr != journal {
		t.Error("Close did not restore os.Stderr")
	}

	if got := agg.Totals()[KindClientEOF]; got != 2*4*50 {
		t.Errorf("client_eof = %d, want %d", got, 2*4*50)
	}
	if ring.Len() != 2*4*50 {
		t.Errorf("ring holds %d lines, want %d", ring.Len(), 2*4*50)
	}
	raw, err := os.ReadFile(journal.Name())
	if err != nil {
		t.Fatal(err)
	}
	kept := strings.Count(string(raw), "kept line")
	if kept != 2*4*50 || strings.Contains(string(raw), "process connection") {
		t.Errorf("journal holds %d kept lines and noise=%v", kept, strings.Contains(string(raw), "process connection"))
	}

	// A write after Close (a core still shutting down) must fail quietly, not
	// panic or block.
	if _, err := fmt.Fprintln(stale, "ERROR[0009] late line"); err == nil {
		t.Error("a write to the closed pipe succeeded")
	}
}

func TestCloseWithoutInterceptIsANoOp(t *testing.T) {
	c := NewCapture(NewRing(1), NewAggregator(), io.Discard)
	c.Close()
}
