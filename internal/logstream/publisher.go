package logstream

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// publishInterval is how often new ring entries are mirrored into Redis.
const publishInterval = time.Second

// publishTimeout bounds one publish round trip, so a hung Redis costs a
// skipped tick rather than a stuck goroutine.
const publishTimeout = 3 * time.Second

// pushChunk caps how many entries go into one RPUSH command; a full ring is
// pushed as several commands inside the same pipeline.
const pushChunk = 500

// Publisher mirrors a Ring into the capped Redis list logs:<source>.
//
// It only ever reads the ring, so it cannot slow the logging path. A failed
// publish leaves its cursor where it was and the same entries are offered again
// next tick; because the ring itself is bounded, an outage of any length keeps
// at most one ring's worth of backlog and older lines are simply lost.
type Publisher struct {
	rdb    redis.Cmdable
	source string
	ring   *Ring
	logger *slog.Logger

	last    int64 // newest id already pushed
	failing bool  // true between the first failed publish and the next good one
}

// NewPublisher returns a publisher for source. logger may be nil; it is only
// used to report an outage once when it starts and once when it ends (never
// per failed tick, since those lines would themselves be published).
func NewPublisher(rdb redis.Cmdable, source string, ring *Ring, logger *slog.Logger) *Publisher {
	return &Publisher{rdb: rdb, source: source, ring: ring, logger: logger}
}

// Run publishes every second until ctx ends, then makes one last attempt so
// the shutdown lines are not lost.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(publishInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = p.Flush(flushCtx)
			cancel()
			return
		case <-ticker.C:
			flushCtx, cancel := context.WithTimeout(ctx, publishTimeout)
			err := p.Flush(flushCtx)
			cancel()
			p.report(err)
		}
	}
}

// report logs a state change (up -> down, down -> up), never a steady state.
func (p *Publisher) report(err error) {
	if p.logger == nil {
		return
	}
	switch {
	case err != nil && !p.failing:
		p.failing = true
		p.logger.Warn("log stream: publishing to redis failed, retrying every second", "source", p.source, "error", err)
	case err == nil && p.failing:
		p.failing = false
		p.logger.Info("log stream: publishing to redis recovered", "source", p.source)
	}
}

// Flush pushes every ring entry newer than the last successful push, trimming
// the list to its newest RingSize elements, in one pipelined round trip. It
// does nothing when there is nothing new.
func (p *Publisher) Flush(ctx context.Context) error {
	entries := p.ring.After(p.last, 0)
	if len(entries) == 0 {
		return nil
	}
	// Anything beyond one list's worth would be trimmed straight away.
	if len(entries) > RingSize {
		entries = entries[len(entries)-RingSize:]
	}
	key := Key(p.source)
	pipe := p.rdb.Pipeline()
	for start := 0; start < len(entries); start += pushChunk {
		end := min(start+pushChunk, len(entries))
		vals := make([]any, 0, end-start)
		for _, e := range entries[start:end] {
			vals = append(vals, e.Encode())
		}
		pipe.RPush(ctx, key, vals...)
	}
	pipe.LTrim(ctx, key, -RingSize, -1)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	p.last = entries[len(entries)-1].ID
	return nil
}
