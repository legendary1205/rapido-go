// Package logstream makes a process' own log output readable from the
// dashboard: an slog handler that keeps a bounded in-memory ring of every
// line it writes, and a publisher that mirrors new ring entries into a capped
// Redis list once a second so any panel process can serve them.
//
// The ring is what keeps this off the logging path. Emitting a line costs one
// short mutex-guarded append; nothing here ever waits on Redis, and when Redis
// is down the ring simply overwrites its own oldest entries instead of
// growing.
package logstream

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RingSize is how many entries the in-process ring and every Redis list keep.
const RingSize = 2000

// MaxLineBytes bounds one stored line. A log line is normally a few hundred
// bytes; the cap only exists so a pathological one cannot bloat a ring slot or
// a Redis element.
const MaxLineBytes = 16 << 10

// Level names as they appear in Entry.Level, lowest to highest severity.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Entry is one stored log line. It is also the exact JSON element shape kept
// in the Redis lists (logs:<source>, logs:node:<id>): ID is microseconds since
// the epoch at line creation and strictly increases within one process, Line is
// the original JSON log line (plain text for a node).
type Entry struct {
	ID    int64  `json:"id"`
	TS    string `json:"ts"`
	Level string `json:"level"`
	Line  string `json:"line"`
}

// LevelRank orders level names for a minimum-level filter. ok is false for a
// name that is not a level.
func LevelRank(level string) (rank int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case LevelDebug, "trace":
		return 0, true
	case LevelInfo:
		return 1, true
	case LevelWarn, "warning":
		return 2, true
	case LevelError, "fatal", "panic":
		return 3, true
	}
	return 0, false
}

// NormalizeLevel maps any spelling LevelRank understands to one of the four
// level constants, and anything else to info.
func NormalizeLevel(level string) string {
	rank, ok := LevelRank(level)
	if !ok {
		return LevelInfo
	}
	switch rank {
	case 0:
		return LevelDebug
	case 2:
		return LevelWarn
	case 3:
		return LevelError
	}
	return LevelInfo
}

// FromSlogLevel is the entry level for an slog record's level.
func FromSlogLevel(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return LevelDebug
	case l < slog.LevelWarn:
		return LevelInfo
	case l < slog.LevelError:
		return LevelWarn
	default:
		return LevelError
	}
}

// Matches reports whether e passes a minimum level (minRank, 0 = everything)
// and a case-insensitive substring filter (needle must already be lower-cased;
// empty matches everything).
func (e Entry) Matches(minRank int, needle string) bool {
	if minRank > 0 {
		if rank, _ := LevelRank(e.Level); rank < minRank {
			return false
		}
	}
	return needle == "" || strings.Contains(strings.ToLower(e.Line), needle)
}

// Encode is the JSON element stored in a Redis list.
func (e Entry) Encode() string {
	b, _ := json.Marshal(e)
	return string(b)
}

// Decode parses one Redis list element; ok is false for anything that is not a
// well-formed entry with a positive id.
func Decode(raw string) (Entry, bool) {
	var e Entry
	if err := json.Unmarshal([]byte(raw), &e); err != nil || e.ID <= 0 {
		return Entry{}, false
	}
	return e, true
}

// IDFromTime is the entry id for an instant.
func IDFromTime(t time.Time) int64 { return t.UnixMicro() }

// TSFromID is the RFC3339Nano timestamp an entry with this id would carry.
func TSFromID(id int64) string {
	return time.UnixMicro(id).UTC().Format(time.RFC3339Nano)
}

// Ring is a fixed-capacity, thread-safe log buffer. Ids handed out by Add are
// strictly increasing, so the ring is always sorted by id and After can
// binary-search it.
type Ring struct {
	mu     sync.Mutex
	buf    []Entry
	start  int // index of the oldest entry
	n      int // entries held
	lastID int64
}

// NewRing returns an empty ring holding at most capacity entries.
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = RingSize
	}
	return &Ring{buf: make([]Entry, capacity)}
}

// Add appends a line stamped at t (zero = now) and returns the stored entry.
// The id is t in microseconds, bumped past the previous id when two lines land
// in the same microsecond or the clock steps backwards, so ids never repeat or
// go down within the process.
func (r *Ring) Add(t time.Time, level, line string) Entry {
	if t.IsZero() {
		t = time.Now()
	}
	t = t.UTC()
	if len(line) > MaxLineBytes {
		line = strings.ToValidUTF8(line[:MaxLineBytes], "") + "..."
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	id := IDFromTime(t)
	if id <= r.lastID {
		id = r.lastID + 1
	}
	r.lastID = id
	e := Entry{ID: id, TS: t.Format(time.RFC3339Nano), Level: level, Line: line}
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = e
		r.n++
	} else {
		r.buf[r.start] = e
		r.start = (r.start + 1) % len(r.buf)
	}
	return e
}

// at is the i-th oldest entry; the caller holds mu.
func (r *Ring) at(i int) Entry { return r.buf[(r.start+i)%len(r.buf)] }

// After returns the entries with id > after, oldest first, at most limit of
// them (limit <= 0 means no cap).
func (r *Ring) After(after int64, limit int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	first := sort.Search(r.n, func(i int) bool { return r.at(i).ID > after })
	count := r.n - first
	if limit > 0 && count > limit {
		count = limit
	}
	out := make([]Entry, count)
	for i := range out {
		out[i] = r.at(first + i)
	}
	return out
}

// Tail returns the newest n entries, oldest first.
func (r *Ring) Tail(n int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > r.n || n <= 0 {
		n = r.n
	}
	out := make([]Entry, n)
	for i := range out {
		out[i] = r.at(r.n - n + i)
	}
	return out
}

// Len is how many entries the ring currently holds.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// LastID is the newest id handed out, 0 while the ring has never been written.
func (r *Ring) LastID() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastID
}

// Process-wide ring and source name. A process has exactly one log output, so
// one shared ring is the natural shape: cmd/panel tees its logger into it and
// the HTTP layer reads it back as the Redis-down fallback.
var (
	processRing = NewRing(RingSize)

	sourceMu      sync.RWMutex
	processSource = "panel"
)

// ProcessRing is the ring this process' logger writes into.
func ProcessRing() *Ring { return processRing }

// SetProcessSource names this process' log stream ("panel" for the API role,
// "backend" for the background role).
func SetProcessSource(source string) {
	sourceMu.Lock()
	processSource = source
	sourceMu.Unlock()
}

// ProcessSource is the source name this process publishes under.
func ProcessSource() string {
	sourceMu.RLock()
	defer sourceMu.RUnlock()
	return processSource
}

// Redis key names, shared by the publisher here and the HTTP handlers.

// Key is the capped list holding a source's entries ("panel", "backend",
// "node:<id>").
func Key(source string) string { return "logs:" + source }

// NodeSource is the source name of one node's stream.
func NodeSource(nodeID int32) string { return "node:" + strconv.Itoa(int(nodeID)) }

// NodeSeqKey holds the newest id stored for a node, so a re-sent batch is
// recognised and skipped.
func NodeSeqKey(nodeID int32) string { return "logs:nodeseq:" + strconv.Itoa(int(nodeID)) }

// WatchKey exists (short TTL) while someone is looking at a node's logs; the
// node asks for it in every node-live response and streams while it is set.
func WatchKey(nodeID int32) string { return "logs:watch:node:" + strconv.Itoa(int(nodeID)) }
