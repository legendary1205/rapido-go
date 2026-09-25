package nodelog

import (
	"bufio"
	"io"
	"os"
	"sync"
	"time"
)

// Capture sits between the sing-box core and the journal. The core writes its log
// to os.Stderr, so InterceptStderr swaps that for a pipe; Capture reads it line
// by line, drops what Classify calls benign (counting it in the Aggregator),
// keeps the rest in the Ring, and writes it on - unchanged - to the real stderr,
// which is what journald reads.
//
// One pipe serves the whole process. sing-box captures os.Stderr each time a core
// is built, so every core the node ever starts (a config change stops and rebuilds
// it) writes to this same pipe: a restart neither reopens nor leaks anything, and
// the pipe is closed exactly once, at shutdown.
type Capture struct {
	ring *Ring
	agg  *Aggregator
	out  io.Writer

	// lastLevel is the level of the previous parsed line: a line without a sing-box
	// prefix (a wrapped message) is filed under it. Only the reading goroutine
	// touches it.
	lastLevel string

	pr, pw    *os.File
	real      *os.File
	done      chan struct{}
	closeOnce sync.Once
}

// NewCapture builds a Capture that writes kept lines to out. Feed it with
// Process; InterceptStderr does that from a pipe.
func NewCapture(ring *Ring, agg *Aggregator, out io.Writer) *Capture {
	return &Capture{ring: ring, agg: agg, out: out, lastLevel: LevelInfo}
}

// InterceptStderr replaces os.Stderr with a pipe read by a new Capture that
// forwards to the real stderr. Do it before any sing-box core is created, and
// call Close at shutdown.
//
// Only the os.Stderr variable is replaced, not file descriptor 2: a Go runtime
// crash (which the journal must always get, in full) is written straight to the
// descriptor and never goes through here.
func InterceptStderr(ring *Ring, agg *Aggregator) (*Capture, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c := NewCapture(ring, agg, os.Stderr)
	c.pr, c.pw, c.real, c.done = pr, pw, os.Stderr, make(chan struct{})
	os.Stderr = pw
	go func() {
		defer close(c.done)
		c.Process(pr)
	}()
	return c, nil
}

// Close puts the real stderr back, closes the pipe and waits (briefly) for the
// reader to finish what is already in it. Safe to call more than once. A write
// to the closed pipe by a core that is still shutting down fails silently, which
// is what a closed log should do.
func (c *Capture) Close() {
	c.closeOnce.Do(func() {
		if c.pw == nil {
			return
		}
		if os.Stderr == c.pw {
			os.Stderr = c.real
		}
		c.pw.Close()
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
		}
		c.pr.Close()
	})
}

// Process reads lines from r until it ends, handling each.
func (c *Capture) Process(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	var long []byte
	for {
		frag, isPrefix, err := br.ReadLine()
		if err != nil {
			if len(long) > 0 {
				c.handle(string(long))
			}
			return
		}
		if isPrefix {
			// A line longer than the buffer: keep its beginning, drop the rest.
			if len(long) < maxLineBytes {
				long = append(long, frag...)
			}
			continue
		}
		if len(long) > 0 {
			c.handle(string(append(long, frag...)))
			long = long[:0]
			continue
		}
		c.handle(string(frag))
	}
}

// handle files one line. It must never lose a line to a bug of its own, so a
// panic falls back to forwarding the raw line.
func (c *Capture) handle(raw string) {
	defer func() {
		if recover() != nil {
			c.forward(raw)
		}
	}()
	line := StripANSI(raw)
	level, text, parsed := ParseLine(line)
	if parsed {
		c.lastLevel = level
		if kind, benign := Classify(level, text); benign {
			c.agg.Count(kind)
			return
		}
	} else {
		level = c.lastLevel
	}
	c.ring.Add(level, text)
	c.forward(raw)
}

func (c *Capture) forward(raw string) {
	c.out.Write([]byte(raw + "\n"))
}
