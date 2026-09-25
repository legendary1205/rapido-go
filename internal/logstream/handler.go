package logstream

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Handler is a JSON slog handler that also records every line it emits into a
// Ring. The bytes written to the destination are exactly what
// slog.NewJSONHandler would write on its own.
type Handler struct {
	inner slog.Handler
	core  *core
}

// core is the state shared by a handler and every handler derived from it
// through WithAttrs/WithGroup: the destination, the ring, and the record
// currently being rendered.
type core struct {
	// mu serializes Handle. The JSON handler renders a record and then calls
	// Write with the finished line, which by itself says nothing about the
	// record's level or time; holding mu across the call is what lets Write
	// pick those up from level/at. Rendering is microseconds and the JSON
	// handler serializes its output write anyway, so this adds no contention
	// worth measuring.
	mu    sync.Mutex
	out   io.Writer
	ring  *Ring
	level string
	at    time.Time
}

// Write receives one complete JSON line (with its trailing newline).
func (c *core) Write(p []byte) (int, error) {
	n, err := c.out.Write(p)
	c.ring.Add(c.at, c.level, strings.TrimRight(string(p), "\r\n"))
	return n, err
}

// NewHandler returns a JSON handler writing to out with opts, whose lines are
// also appended to ring.
func NewHandler(out io.Writer, opts *slog.HandlerOptions, ring *Ring) *Handler {
	c := &core{out: out, ring: ring}
	return &Handler{inner: slog.NewJSONHandler(c, opts), core: c}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	h.core.mu.Lock()
	defer h.core.mu.Unlock()
	h.core.level = FromSlogLevel(r.Level)
	h.core.at = r.Time
	return h.inner.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(attrs), core: h.core}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), core: h.core}
}
