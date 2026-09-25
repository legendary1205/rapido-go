package nodelog

import (
	"bytes"
	"io"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

// DefaultRingSize is how many lines the node keeps for the panel to fetch.
const DefaultRingSize = 5000

// maxLineBytes bounds one stored line; nothing sing-box or the agent logs comes
// near it, but the buffer is sized in lines and must stay bounded in bytes too.
const maxLineBytes = 16 << 10

// Entry is one stored log line.
type Entry struct {
	// ID is microseconds since the epoch at creation, and strictly increases for
	// the life of the ring - also when the clock steps back - so a reader can
	// resume with "everything after this id". Because it is a wall-clock value
	// it keeps increasing across process restarts as well.
	ID    int64
	Level string // debug, info, warn or error
	Text  string // the line, without colour codes or its trailing newline
}

// Time is when the line was logged, derived from its id.
func (e Entry) Time() time.Time { return time.UnixMicro(e.ID).UTC() }

// Ring is a fixed-size, thread-safe buffer of the newest log lines.
type Ring struct {
	mu    sync.Mutex
	buf   []Entry
	start int // index of the oldest entry
	n     int // entries held
	last  int64
	now   func() time.Time
}

// NewRing makes a ring holding up to capacity lines (DefaultRingSize if not
// positive).
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultRingSize
	}
	return &Ring{buf: make([]Entry, capacity), now: time.Now}
}

// Add stores a line, dropping the oldest when full, and returns its id.
func (r *Ring) Add(level, text string) int64 {
	if len(text) > maxLineBytes {
		cut := maxLineBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "...(truncated)"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.now().UnixMicro()
	if id <= r.last {
		id = r.last + 1
	}
	r.last = id
	e := Entry{ID: id, Level: level, Text: text}
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = e
		r.n++
	} else {
		r.buf[r.start] = e
		r.start = (r.start + 1) % len(r.buf)
	}
	return id
}

func (r *Ring) at(i int) Entry { return r.buf[(r.start+i)%len(r.buf)] }

// firstAfter is the logical index of the oldest entry with an id above after.
// Ids increase with the index, so a binary search finds it.
func (r *Ring) firstAfter(after int64) int {
	return sort.Search(r.n, func(i int) bool { return r.at(i).ID > after })
}

// Since returns the oldest limit entries with an id greater than after, oldest
// first. A caller that keeps passing the last id it received therefore walks
// forward through everything still held without gaps.
func (r *Ring) Since(after int64, limit int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	from := r.firstAfter(after)
	count := r.n - from
	if limit >= 0 && count > limit {
		count = limit
	}
	out := make([]Entry, 0, count)
	for i := from; i < from+count; i++ {
		out = append(out, r.at(i))
	}
	return out
}

// Newest returns the newest n entries, oldest first.
func (r *Ring) Newest(n int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > r.n {
		n = r.n
	}
	if n < 0 {
		n = 0
	}
	out := make([]Entry, 0, n)
	for i := r.n - n; i < r.n; i++ {
		out = append(out, r.at(i))
	}
	return out
}

// OldestID is the id of the oldest line still held, 0 when empty.
func (r *Ring) OldestID() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return 0
	}
	return r.at(0).ID
}

// NewestID is the id of the newest line, 0 when nothing was ever added.
func (r *Ring) NewestID() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// Len is how many lines are held.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// jsonTee is an io.Writer for the agent's own slog JSON handler: it passes every
// write on to the real destination and records the lines in the ring.
type jsonTee struct {
	dst io.Writer
	r   *Ring
}

// TeeJSON returns a writer that forwards to dst and records each line written
// into r, with the level read from the JSON record. slog's JSON handler makes one
// Write per record, but a write holding several lines is split all the same.
func (r *Ring) TeeJSON(dst io.Writer) io.Writer {
	return &jsonTee{dst: dst, r: r}
}

func (t *jsonTee) Write(p []byte) (int, error) {
	n, err := t.dst.Write(p)
	for _, line := range bytes.Split(p, []byte{'\n'}) {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			continue
		}
		t.r.Add(jsonLevel(line), string(line))
	}
	return n, err
}

var levelKey = []byte(`"level":"`)

// jsonLevel reads the level of a slog JSON record. The handler writes it right
// after the time, so it is found within the first few dozen bytes; a record that
// has none is taken as info.
func jsonLevel(line []byte) string {
	head := line
	if len(head) > 160 {
		head = head[:160]
	}
	i := bytes.Index(head, levelKey)
	if i < 0 {
		return LevelInfo
	}
	v := head[i+len(levelKey):]
	switch {
	case bytes.HasPrefix(v, []byte("ERROR")):
		return LevelError
	case bytes.HasPrefix(v, []byte("WARN")):
		return LevelWarn
	case bytes.HasPrefix(v, []byte("DEBUG")):
		return LevelDebug
	}
	return LevelInfo
}
