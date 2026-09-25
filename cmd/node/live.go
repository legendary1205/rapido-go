package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/legendary1205/rapido-go/internal/nodelog"
)

// The live channel: POST /api/internal/node-live, a second, faster report next
// to node-report (which keeps its own schedule and content). It carries what
// changes by the second - who is connected right now, how many connections each
// port holds, the counters of dropped log noise - and, while an admin is
// watching this node's log in the panel, the log lines themselves.
//
// It runs in its own loop with its own client and a short timeout, so a slow or
// missing panel can never delay the usage/config sync.
const (
	liveInterval       = 5 * time.Second
	liveStreamInterval = time.Second
	liveRetryAfter404  = 30 * time.Second
	liveWarnEvery      = time.Minute
	liveTimeout        = 4 * time.Second
	// liveGzipOver is the body size above which the request is compressed. A
	// full snapshot of thousands of online users is large and repetitive.
	liveGzipOver = 1024
	// liveLogsMax caps the lines in one request; liveLogsFirst is how many of the
	// newest lines a panel that has none of ours yet is sent.
	liveLogsMax   = 500
	liveLogsFirst = 200
	liveMinWait   = 250 * time.Millisecond
	liveMaxWait   = 60 * time.Second
)

// liveFirstDelay is the wait before the first report: soon after start rather
// than a full interval later, so the panel learns of a restarted node's clients
// at once. A variable only so tests need not wait for it.
var liveFirstDelay = time.Second

// defaultLiveStatePath is where the agent leaves its latest live snapshot for
// `rapido-go-node status` to read (it is a different process, and reading it
// costs no round trip to the panel). /run is a tmpfs: nothing survives a reboot.
const defaultLiveStatePath = "/run/rapido-go-node-live.json"

type liveLog struct {
	ID    int64  `json:"id"`
	TS    string `json:"ts"`
	Level string `json:"level"`
	Line  string `json:"line"`
}

type liveRequest struct {
	ConnsTotal int64            `json:"conns_total"`
	PortConns  map[string]int64 `json:"port_conns"`
	// Online is [username, open connections] for everyone connected right now.
	Online     [][2]any         `json:"online"`
	Suppressed map[string]int64 `json:"suppressed"`
	Logs       []liveLog        `json:"logs,omitempty"`
}

type liveResponse struct {
	LogStream  bool  `json:"log_stream"`
	LogAfter   int64 `json:"log_after"`
	IntervalMS int   `json:"interval_ms"`
}

// liveSnapshot is the file `status` reads.
type liveSnapshot struct {
	At          time.Time        `json:"at"`
	ConnsTotal  int64            `json:"conns_total"`
	UsersOnline int              `json:"users_online"`
	PortConns   map[string]int64 `json:"port_conns"`
}

// liveReporter is one live loop's state: what the panel last told it, and when
// it last complained.
type liveReporter struct {
	s      *server
	url    string
	secret string
	client *http.Client
	now    func() time.Time

	// From the previous response: whether to send log lines, and the newest one
	// the panel already holds.
	logStream bool
	logAfter  int64
	lastWarn  time.Time
}

func (s *server) newLiveReporter(cfg config) *liveReporter {
	return &liveReporter{
		s:      s,
		url:    cfg.PanelURL + "/api/internal/node-live",
		secret: cfg.ReportSecret,
		client: &http.Client{Timeout: liveTimeout},
		now:    time.Now,
	}
}

// liveLoop reports on the live channel for the whole life of the process, like
// syncLoop and independent of it and of whether a core is running.
func (s *server) liveLoop(ctx context.Context, cfg config) {
	l := s.newLiveReporter(cfg)
	timer := time.NewTimer(liveFirstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(l.tick(ctx))
	}
}

// tick sends one report and returns how long to wait before the next.
func (l *liveReporter) tick(ctx context.Context) time.Duration {
	req := l.s.buildLiveRequest(l.logStream, l.logAfter)
	l.s.writeLiveState(req)

	body, err := json.Marshal(req)
	if err != nil {
		l.warn("live report: marshal", "error", err)
		return liveInterval
	}
	gzipped := false
	if len(body) > liveGzipOver {
		if zipped, ok := gzipBytes(body); ok {
			body, gzipped = zipped, true
		}
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url, bytes.NewReader(body))
	if err != nil {
		l.warn("live report: build request", "error", err)
		return liveInterval
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+l.secret)
	if gzipped {
		hreq.Header.Set("Content-Encoding", "gzip")
	}

	resp, err := l.client.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return liveInterval
		}
		l.failed()
		l.warn("live report: request failed, will retry", "error", err)
		return liveInterval
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// A panel that predates this endpoint. Ask again now and then: it may be
		// upgraded while this node keeps running.
		l.failed()
		l.warn("live report: the panel has no node-live endpoint (older panel?), retrying rarely", "retry_in", liveRetryAfter404.String())
		return liveRetryAfter404
	case resp.StatusCode != http.StatusOK:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		l.failed()
		l.warn("live report: panel rejected report", "status", resp.StatusCode)
		return liveInterval
	}

	var out liveResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		l.failed()
		l.warn("live report: unreadable response", "error", err)
		return liveInterval
	}
	l.logStream, l.logAfter = out.LogStream, out.LogAfter
	return l.nextWait(out)
}

// failed forgets the panel's last answer: with the panel in doubt, stop
// streaming and fall back to the slow schedule. Nothing is lost - the ring still
// holds the lines, and the next good answer says which ones the panel has.
func (l *liveReporter) failed() {
	l.logStream = false
}

// nextWait is the panel's interval_ms when it gave a sane one, else 1s while a
// log is being streamed and 5s otherwise.
func (l *liveReporter) nextWait(out liveResponse) time.Duration {
	if out.IntervalMS > 0 {
		return min(max(time.Duration(out.IntervalMS)*time.Millisecond, liveMinWait), liveMaxWait)
	}
	if out.LogStream {
		return liveStreamInterval
	}
	return liveInterval
}

// warn logs at most once a minute: a panel that is down or old would otherwise
// fill the log with this every few seconds.
func (l *liveReporter) warn(msg string, args ...any) {
	if now := l.now(); l.lastWarn.IsZero() || now.Sub(l.lastWarn) >= liveWarnEvery {
		l.lastWarn = now
		l.s.logger.Warn(msg, args...)
	}
}

func gzipBytes(b []byte) ([]byte, bool) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, false
	}
	if _, err := zw.Write(b); err != nil {
		return nil, false
	}
	if err := zw.Close(); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// buildLiveRequest takes the snapshot for one report. Log lines are included only
// while the panel is streaming.
func (s *server) buildLiveRequest(stream bool, logAfter int64) liveRequest {
	req := liveRequest{
		PortConns:  map[string]int64{},
		Online:     [][2]any{},
		Suppressed: map[string]int64{},
	}
	if s.traffic != nil {
		snap := s.traffic.Presence()
		req.ConnsTotal = snap.Total
		for port, n := range snap.Ports {
			req.PortConns[strconv.Itoa(int(port))] = n
		}
		for _, u := range snap.Users {
			req.Online = append(req.Online, [2]any{u.User, u.Conns})
		}
	}
	if s.suppressed != nil {
		for kind, n := range s.suppressed.Totals() {
			req.Suppressed[kind] = n
		}
	}
	if stream && s.ring != nil {
		for _, e := range selectLiveLogs(s.ring, logAfter) {
			req.Logs = append(req.Logs, liveLog{
				ID:    e.ID,
				TS:    e.Time().Format("2006-01-02T15:04:05.000Z"),
				Level: e.Level,
				Line:  e.Text,
			})
		}
	}
	return req
}

// selectLiveLogs picks the lines to send: everything after what the panel has,
// oldest first and capped, so a panel that is behind catches up over successive
// requests without gaps. When the panel has nothing of ours, or what it has is
// older than the oldest line still held (so lines in between are gone), it gets
// the newest few instead of a stale backlog.
func selectLiveLogs(ring *nodelog.Ring, after int64) []nodelog.Entry {
	if after <= 0 || after < ring.OldestID() {
		return ring.Newest(liveLogsFirst)
	}
	return ring.Since(after, liveLogsMax)
}

// writeLiveState leaves the latest counts where `status` can read them.
// Best effort: this is a convenience, never a reason to log or fail.
func (s *server) writeLiveState(req liveRequest) {
	if s.liveStateFile == "" {
		return
	}
	raw, err := json.Marshal(liveSnapshot{
		At:          time.Now().UTC(),
		ConnsTotal:  req.ConnsTotal,
		UsersOnline: len(req.Online),
		PortConns:   req.PortConns,
	})
	if err != nil {
		return
	}
	tmp := s.liveStateFile + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) != nil {
		return
	}
	if os.Rename(tmp, s.liveStateFile) != nil {
		os.Remove(tmp)
	}
}
