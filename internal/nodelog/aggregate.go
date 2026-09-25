package nodelog

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// FlushMessage is the one line the aggregator logs in place of the dropped ones.
const FlushMessage = "suppressed benign client errors"

// FlushInterval is how often that line is emitted (when anything was dropped).
const FlushInterval = time.Minute

// Aggregator counts the dropped noise by kind. Each flush turns what was dropped
// since the previous one into a single INFO line; the cumulative totals since
// start stay available for the node's live report.
type Aggregator struct {
	mu     sync.Mutex
	window map[string]int64
	total  map[string]int64
	since  time.Time
	now    func() time.Time
}

func NewAggregator() *Aggregator {
	return &Aggregator{window: map[string]int64{}, total: map[string]int64{}, now: time.Now, since: time.Now()}
}

// Count records one dropped line of the given kind.
func (a *Aggregator) Count(kind string) {
	a.mu.Lock()
	a.window[kind]++
	a.total[kind]++
	a.mu.Unlock()
}

// Totals returns the cumulative count per kind since the node started - a copy,
// never nil.
func (a *Aggregator) Totals() map[string]int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]int64, len(a.total))
	for k, v := range a.total {
		out[k] = v
	}
	return out
}

// Flush logs one INFO line with the counts dropped since the last flush, and
// starts a new window. When nothing was dropped it logs nothing.
func (a *Aggregator) Flush(logger *slog.Logger) {
	a.mu.Lock()
	window := a.window
	a.window = map[string]int64{}
	now := a.now()
	elapsed := now.Sub(a.since)
	a.since = now
	a.mu.Unlock()
	if len(window) == 0 {
		return
	}

	kinds := make([]string, 0, len(window))
	var sum int64
	for k, v := range window {
		kinds = append(kinds, k)
		sum += v
	}
	sort.Strings(kinds)
	attrs := make([]slog.Attr, 0, len(kinds)+2)
	attrs = append(attrs, slog.Int64("total", sum), slog.Int("window_seconds", int(elapsed.Round(time.Second)/time.Second)))
	for _, k := range kinds {
		attrs = append(attrs, slog.Int64(k, window[k]))
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, FlushMessage, attrs...)
}

// Run flushes every interval (FlushInterval when not positive) until ctx ends,
// then once more so the last partial window is not lost.
func (a *Aggregator) Run(ctx context.Context, logger *slog.Logger, interval time.Duration) {
	if interval <= 0 {
		interval = FlushInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.Flush(logger)
			return
		case <-ticker.C:
			a.Flush(logger)
		}
	}
}
