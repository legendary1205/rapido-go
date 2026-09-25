package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
	"github.com/legendary1205/rapido-go/internal/nodelog"
)

// syncedBuffer is a log sink the loop goroutine and the test can share.
type syncedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncedBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncedBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncedBuffer) count(sub string) int {
	return strings.Count(s.String(), sub)
}

// capturedLive is one request the fake panel received.
type capturedLive struct {
	Method, Path string
	Header       http.Header
	Gzipped      bool
	RawSize      int
	Body         liveRequest
	Fields       map[string]json.RawMessage
}

// fakeLivePanel stands in for the panel's node-live endpoint.
type fakeLivePanel struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []capturedLive
	// reply decides the answer to request number n (1-based).
	reply func(n int) (status int, body string)
}

func newFakeLivePanel(t *testing.T, reply func(n int) (int, string)) *fakeLivePanel {
	t.Helper()
	p := &fakeLivePanel{reply: reply}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c := capturedLive{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), RawSize: len(raw)}
		if r.Header.Get("Content-Encoding") == "gzip" {
			c.Gzipped = true
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Errorf("request claims gzip but is not: %v", err)
			} else if raw, err = io.ReadAll(zr); err != nil {
				t.Errorf("gunzip request: %v", err)
			}
		}
		if err := json.Unmarshal(raw, &c.Body); err != nil {
			t.Errorf("request body is not the live JSON: %v\n%s", err, raw)
		}
		json.Unmarshal(raw, &c.Fields)
		p.mu.Lock()
		p.reqs = append(p.reqs, c)
		n := len(p.reqs)
		p.mu.Unlock()
		status, body := p.reply(n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fakeLivePanel) requests() []capturedLive {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedLive(nil), p.reqs...)
}

// requestsLocked is requests for a reply func, which runs while the test holds
// its own lock but not the panel's.
func (p *fakeLivePanel) requestsLocked() []capturedLive { return p.requests() }

func liveJSON(stream bool, after int64, intervalMS int) string {
	return fmt.Sprintf(`{"log_stream":%v,"log_after":%d,"interval_ms":%d}`, stream, after, intervalMS)
}

type liveRig struct {
	srv    *server
	rep    *liveReporter
	logs   *syncedBuffer
	clock  time.Time
	ring   *nodelog.Ring
	supp   *nodelog.Aggregator
	traffc *traffic.Manager
}

func newLiveRig(t *testing.T, panelURL string) *liveRig {
	t.Helper()
	rig := &liveRig{
		logs:   &syncedBuffer{},
		ring:   nodelog.NewRing(nodelog.DefaultRingSize),
		supp:   nodelog.NewAggregator(),
		traffc: traffic.NewManager(),
		clock:  time.Date(2026, 9, 25, 4, 0, 0, 0, time.UTC),
	}
	rig.srv = &server{
		logger:     slog.New(slog.NewJSONHandler(rig.logs, nil)),
		traffic:    rig.traffc,
		ring:       rig.ring,
		suppressed: rig.supp,
	}
	rig.rep = rig.srv.newLiveReporter(config{PanelURL: panelURL, ReportSecret: testSecret})
	rig.rep.now = func() time.Time { return rig.clock }
	return rig
}

func (r *liveRig) tick(t *testing.T) time.Duration {
	t.Helper()
	return r.rep.tick(context.Background())
}

func TestLiveRequestShapeAndAuth(t *testing.T) {
	panel := newFakeLivePanel(t, func(int) (int, string) { return 200, liveJSON(false, 0, 5000) })
	rig := newLiveRig(t, panel.URL)

	endA1 := rig.traffc.OpenConn("alice", 20000)
	endA2 := rig.traffc.OpenConn("alice", 20001)
	endB := rig.traffc.OpenConn("bob", 20000)
	defer endA1()
	defer endA2()
	defer endB()
	for range 5787 {
		rig.supp.Count(nodelog.KindClientEOF)
	}
	rig.supp.Count(nodelog.KindDialTimeout + ":germany~wg")
	rig.ring.Add(nodelog.LevelError, "must not be sent while nobody watches")

	if wait := rig.tick(t); wait != 5*time.Second {
		t.Errorf("wait after a 5000 ms answer = %v, want 5s", wait)
	}

	reqs := panel.requests()
	if len(reqs) != 1 {
		t.Fatalf("panel got %d requests", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Path != "/api/internal/node-live" {
		t.Errorf("request = %s %s", r.Method, r.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testSecret {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if r.Gzipped {
		t.Error("a tiny body was compressed")
	}
	if r.Body.ConnsTotal != 3 {
		t.Errorf("conns_total = %d, want 3", r.Body.ConnsTotal)
	}
	if r.Body.PortConns["20000"] != 2 || r.Body.PortConns["20001"] != 1 || len(r.Body.PortConns) != 2 {
		t.Errorf("port_conns = %v", r.Body.PortConns)
	}
	if string(r.Fields["online"]) != `[["alice",2],["bob",1]]` {
		t.Errorf("online = %s, want [[\"alice\",2],[\"bob\",1]]", r.Fields["online"])
	}
	if r.Body.Suppressed[nodelog.KindClientEOF] != 5787 || r.Body.Suppressed["dial_timeout:germany~wg"] != 1 {
		t.Errorf("suppressed = %v", r.Body.Suppressed)
	}
	if _, present := r.Fields["logs"]; present {
		t.Errorf("logs were sent while the panel is not streaming: %s", r.Fields["logs"])
	}
}

func TestLiveRequestForAnIdleNodeIsAnEmptySnapshotNotNull(t *testing.T) {
	panel := newFakeLivePanel(t, func(int) (int, string) { return 200, liveJSON(false, 0, 5000) })
	rig := newLiveRig(t, panel.URL)
	rig.tick(t)
	f := panel.requests()[0].Fields
	for key, want := range map[string]string{"conns_total": "0", "port_conns": "{}", "online": "[]", "suppressed": "{}"} {
		if string(f[key]) != want {
			t.Errorf("%s = %s, want %s (an empty snapshot must still be a snapshot: it is what tells the panel everyone left)", key, f[key], want)
		}
	}

	// A node with none of the optional parts wired (as in the other tests) works too.
	bare := &server{logger: quietLogger()}
	req := bare.buildLiveRequest(true, 0)
	if req.ConnsTotal != 0 || req.Online == nil || req.PortConns == nil || req.Suppressed == nil || len(req.Logs) != 0 {
		t.Errorf("bare server request = %+v", req)
	}
}

func TestLiveGzipsOnlyLargeBodies(t *testing.T) {
	panel := newFakeLivePanel(t, func(int) (int, string) { return 200, liveJSON(false, 0, 5000) })
	rig := newLiveRig(t, panel.URL)

	var ends []func()
	for i := range 400 {
		ends = append(ends, rig.traffc.OpenConn(fmt.Sprintf("user-%04d", i), uint16(20000+i%3)))
	}
	defer func() {
		for _, e := range ends {
			e()
		}
	}()
	rig.tick(t)

	r := panel.requests()[0]
	if !r.Gzipped || r.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("a body of hundreds of users was not gzipped")
	}
	if len(r.Body.Online) != 400 || r.Body.ConnsTotal != 400 {
		t.Errorf("after gunzip: %d online, %d total", len(r.Body.Online), r.Body.ConnsTotal)
	}
	if plain, _ := json.Marshal(rig.srv.buildLiveRequest(false, 0)); r.RawSize >= len(plain) {
		t.Errorf("compressed body is %d bytes, the plain one %d", r.RawSize, len(plain))
	}
}

func TestLiveLogStreamingAndLogAfter(t *testing.T) {
	// A panel that stores what it is sent and reports the newest id it holds, like
	// the real one; streaming is switched by the test.
	var mu sync.Mutex
	stream, after := false, int64(0)
	var panel *fakeLivePanel
	panel = newFakeLivePanel(t, func(int) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		reqs := panel.requestsLocked()
		for _, l := range reqs[len(reqs)-1].Body.Logs {
			after = max(after, l.ID)
		}
		return 200, liveJSON(stream, after, 0)
	})
	setStream := func(s bool) { mu.Lock(); stream = s; mu.Unlock() }
	rig := newLiveRig(t, panel.URL)
	lastLogs := func() []liveLog {
		reqs := panel.requests()
		return reqs[len(reqs)-1].Body.Logs
	}

	for i := 1; i <= 300; i++ {
		rig.ring.Add(nodelog.LevelInfo, fmt.Sprintf("line %d", i))
	}

	// Not streaming: no logs, and the answer's absent interval_ms means 5s.
	if wait := rig.tick(t); wait != liveInterval {
		t.Errorf("idle wait = %v", wait)
	}
	if _, present := panel.requests()[0].Fields["logs"]; present {
		t.Fatal("logs sent before the panel asked")
	}

	// The panel starts streaming: the loop speeds up to 1s. The request that
	// learned of it carries nothing - logs follow only a response that said so.
	setStream(true)
	if wait := rig.tick(t); wait != liveStreamInterval {
		t.Errorf("wait while streaming = %v, want 1s", wait)
	}
	if got := lastLogs(); len(got) != 0 {
		t.Fatalf("the request that learned about streaming already carried %d logs", len(got))
	}

	// The panel holds none of ours (log_after 0): it gets the newest 200,
	// oldest first.
	rig.tick(t)
	logs := lastLogs()
	if len(logs) != liveLogsFirst || logs[0].Line != "line 101" || logs[len(logs)-1].Line != "line 300" {
		t.Fatalf("first batch = %d lines from %q to %q, want the newest 200 (line 101..300)", len(logs), first(logs), last(logs))
	}
	for i := 1; i < len(logs); i++ {
		if logs[i].ID <= logs[i-1].ID {
			t.Fatalf("log ids not increasing at %d", i)
		}
	}
	entry := logs[len(logs)-1]
	if entry.Level != "info" || len(entry.TS) != len("2026-09-25T04:00:00.000Z") || !strings.HasSuffix(entry.TS, "Z") {
		t.Errorf("log entry = %+v", entry)
	}
	if ts, err := time.Parse(time.RFC3339Nano, entry.TS); err != nil || ts.UnixMicro()/1000 != entry.ID/1000 {
		t.Errorf("ts %q does not match id %d (%v)", entry.TS, entry.ID, err)
	}

	// Nothing new: nothing sent (the panel has them all).
	rig.tick(t)
	if got := lastLogs(); len(got) != 0 {
		t.Fatalf("resent %d lines the panel already holds", len(got))
	}

	// New lines follow, and only those, with their levels.
	rig.ring.Add(nodelog.LevelWarn, "line 301")
	rig.ring.Add(nodelog.LevelError, "line 302")
	rig.tick(t)
	got := lastLogs()
	if len(got) != 2 || got[0].Line != "line 301" || got[0].Level != "warn" || got[1].Line != "line 302" || got[1].Level != "error" {
		t.Fatalf("incremental batch = %+v, want exactly the two new lines", got)
	}

	// A burst larger than one request: at most 500 per request, oldest first, no
	// gaps, until the panel has caught up.
	for i := 303; i < 303+1100; i++ {
		rig.ring.Add(nodelog.LevelInfo, fmt.Sprintf("line %d", i))
	}
	var walked []string
	var sizes []int
	for range 6 {
		rig.tick(t)
		batch := lastLogs()
		if len(batch) == 0 {
			break
		}
		sizes = append(sizes, len(batch))
		for _, e := range batch {
			walked = append(walked, e.Line)
		}
	}
	if fmt.Sprint(sizes) != "[500 500 100]" {
		t.Errorf("batch sizes = %v, want [500 500 100]", sizes)
	}
	if len(walked) != 1100 || walked[0] != "line 303" || walked[1099] != "line 1402" {
		t.Fatalf("walked %d lines (%q .. %q), want all 1100 in order", len(walked), firstStr(walked), lastStr(walked))
	}
	for i := 1; i < len(walked); i++ {
		if want := fmt.Sprintf("line %d", 303+i); walked[i] != want {
			t.Fatalf("gap or reorder at %d: got %q want %q", i, walked[i], want)
		}
	}

	// A cursor older than anything the ring still holds is stale: newest 200.
	small := nodelog.NewRing(250)
	for i := 1; i <= 400; i++ {
		small.Add(nodelog.LevelInfo, fmt.Sprintf("s%d", i))
	}
	stale := selectLiveLogs(small, small.OldestID()-1_000_000)
	if len(stale) != liveLogsFirst || stale[len(stale)-1].Text != "s400" {
		t.Errorf("stale cursor: %d lines ending %q, want the newest 200", len(stale), lastEntry(stale))
	}
	if inRange := selectLiveLogs(small, small.OldestID()); len(inRange) != 249 {
		t.Errorf("a cursor at the oldest held line got %d lines, want the 249 after it", len(inRange))
	}
	if none := selectLiveLogs(small, small.NewestID()); len(none) != 0 {
		t.Errorf("a cursor at the newest line got %d lines", len(none))
	}
	if zero := selectLiveLogs(small, 0); len(zero) != liveLogsFirst {
		t.Errorf("cursor 0 got %d lines, want 200", len(zero))
	}

	// The panel stops watching: back to no logs at all, and the slow schedule.
	setStream(false)
	rig.tick(t) // learns of it
	rig.ring.Add(nodelog.LevelInfo, "unwatched line")
	if wait := rig.tick(t); wait != liveInterval {
		t.Errorf("wait after streaming stopped = %v", wait)
	}
	if _, present := panel.requests()[len(panel.requests())-1].Fields["logs"]; present {
		t.Error("logs still sent after the panel stopped streaming")
	}
}

func first(l []liveLog) string {
	if len(l) == 0 {
		return ""
	}
	return l[0].Line
}

func last(l []liveLog) string {
	if len(l) == 0 {
		return ""
	}
	return l[len(l)-1].Line
}

func firstStr(l []string) string { return first2(l, 0) }
func lastStr(l []string) string  { return first2(l, len(l)-1) }
func first2(l []string, i int) string {
	if i < 0 || i >= len(l) {
		return ""
	}
	return l[i]
}

func lastEntry(e []nodelog.Entry) string {
	if len(e) == 0 {
		return ""
	}
	return e[len(e)-1].Text
}

func TestLiveHonoursTheIntervalThePanelReturns(t *testing.T) {
	answers := []string{
		liveJSON(false, 0, 1000),
		liveJSON(false, 0, 5000),
		liveJSON(true, 0, 0),
		liveJSON(false, 0, 0),
		liveJSON(false, 0, 10),        // absurdly small: clamped
		liveJSON(false, 0, 9_999_999), // absurdly large: clamped
		liveJSON(true, 0, 2500),
	}
	want := []time.Duration{time.Second, 5 * time.Second, time.Second, 5 * time.Second, liveMinWait, liveMaxWait, 2500 * time.Millisecond}
	panel := newFakeLivePanel(t, func(n int) (int, string) { return 200, answers[n-1] })
	rig := newLiveRig(t, panel.URL)
	for i, w := range want {
		if got := rig.tick(t); got != w {
			t.Errorf("answer %d (%s): wait = %v, want %v", i+1, answers[i], got, w)
		}
	}
}

// An older panel has no such endpoint: one quiet warning a minute, and a long
// back-off rather than a request every few seconds.
func TestLiveBacksOffAfter404AndWarnsOncePerMinute(t *testing.T) {
	panel := newFakeLivePanel(t, func(n int) (int, string) {
		if n <= 3 {
			return 404, `{"detail":"Not Found"}`
		}
		return 200, liveJSON(true, 0, 1000)
	})
	rig := newLiveRig(t, panel.URL)
	rig.rep.logStream = true // as if a streaming answer had been received earlier

	for i := 0; i < 2; i++ {
		if wait := rig.tick(t); wait != 30*time.Second {
			t.Fatalf("wait after a 404 = %v, want 30s", wait)
		}
		rig.clock = rig.clock.Add(30 * time.Second)
	}
	if n := rig.logs.count(`"level":"WARN"`); n != 1 {
		t.Errorf("%d warnings within a minute of 404s, want 1:\n%s", n, rig.logs.String())
	}
	if rig.rep.logStream {
		t.Error("still streaming after the panel stopped answering the channel")
	}

	rig.clock = rig.clock.Add(31 * time.Second) // a minute since the first warning
	rig.tick(t)
	if n := rig.logs.count(`"level":"WARN"`); n != 2 {
		t.Errorf("%d warnings after a minute, want 2", n)
	}

	// The panel is upgraded: the loop picks the channel up again.
	if wait := rig.tick(t); wait != time.Second {
		t.Errorf("wait once the endpoint exists = %v, want 1s", wait)
	}
	if !rig.rep.logStream {
		t.Error("the channel did not recover")
	}
	if strings.Contains(rig.logs.String(), testSecret) {
		t.Error("the secret reached the log")
	}
}

func TestLiveSurvivesPanelErrors(t *testing.T) {
	statuses := []int{500, 401, 200}
	panel := newFakeLivePanel(t, func(n int) (int, string) {
		if statuses[n-1] == 200 {
			return 200, "this is not json"
		}
		return statuses[n-1], `{"detail":"nope"}`
	})
	rig := newLiveRig(t, panel.URL)
	for range statuses {
		if wait := rig.tick(t); wait != liveInterval {
			t.Errorf("wait after a bad answer = %v, want 5s", wait)
		}
		rig.clock = rig.clock.Add(time.Second)
	}
	if n := rig.logs.count(`"level":"WARN"`); n != 1 {
		t.Errorf("%d warnings for three bad answers in three seconds, want 1", n)
	}
}

func TestLiveSurvivesAnUnreachableAndASlowPanel(t *testing.T) {
	// Nothing listens: a refused connection.
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	rig := newLiveRig(t, url)
	start := time.Now()
	if wait := rig.tick(t); wait != liveInterval {
		t.Errorf("wait after a refused connection = %v", wait)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("a refused connection took %v", time.Since(start))
	}
	if rig.logs.count(`"level":"WARN"`) != 1 {
		t.Errorf("log:\n%s", rig.logs.String())
	}

	// A panel that hangs: the short client timeout ends it.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	rig2 := newLiveRig(t, slow.URL)
	if rig2.rep.client.Timeout <= 0 || rig2.rep.client.Timeout > 10*time.Second {
		t.Fatalf("client timeout = %v, want a short one", rig2.rep.client.Timeout)
	}
	rig2.rep.client.Timeout = 100 * time.Millisecond
	start = time.Now()
	if wait := rig2.tick(t); wait != liveInterval {
		t.Errorf("wait after a timeout = %v", wait)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("a hanging panel held the loop for %v", time.Since(start))
	}
}

// The loop runs on its own schedule for the life of the process and stops when
// the context does.
func TestLiveLoopRunsAndStops(t *testing.T) {
	old := liveFirstDelay
	liveFirstDelay = 10 * time.Millisecond
	defer func() { liveFirstDelay = old }()

	panel := newFakeLivePanel(t, func(int) (int, string) { return 200, liveJSON(true, 0, 20) })
	rig := newLiveRig(t, panel.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rig.srv.liveLoop(ctx, config{PanelURL: panel.URL, ReportSecret: testSecret})
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for len(panel.requests()) < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d requests in 3s", len(panel.requests()))
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("liveLoop did not stop with its context")
	}
	n := len(panel.requests())
	time.Sleep(100 * time.Millisecond)
	if len(panel.requests()) != n {
		t.Error("requests kept arriving after the loop stopped")
	}
}

// What the loop writes for `status` is what `status` reads.
func TestLiveWritesTheStateFileStatusReads(t *testing.T) {
	panel := newFakeLivePanel(t, func(int) (int, string) { return 200, liveJSON(false, 0, 5000) })
	rig := newLiveRig(t, panel.URL)
	rig.srv.liveStateFile = filepath.Join(t.TempDir(), "live.json")

	end1 := rig.traffc.OpenConn("alice", 443)
	end2 := rig.traffc.OpenConn("alice", 443)
	end3 := rig.traffc.OpenConn("bob", 443)
	defer end1()
	defer end2()
	defer end3()
	rig.tick(t)

	raw, err := os.ReadFile(rig.srv.liveStateFile)
	if err != nil {
		t.Fatal(err)
	}
	h := newCLIHost(t)
	h.c.paths.Live = rig.srv.liveStateFile
	h.c.now = time.Now // the agent stamps its snapshot with the real clock
	if got := h.c.clientsLine(); got != "2 users online, 3 connections open" {
		t.Errorf("status reads %q from %s", got, raw)
	}
	if _, err := os.Stat(rig.srv.liveStateFile + ".tmp"); err == nil {
		t.Error("a temp file was left behind")
	}

	// No path configured, or an unwritable one: nothing happens, nothing fails.
	rig.srv.liveStateFile = ""
	rig.tick(t)
	rig.srv.liveStateFile = filepath.Join(t.TempDir(), "no", "such", "dir", "live.json")
	rig.tick(t)
}

// Through the whole agent path: a real client of each protocol on a multi-port
// inbound shows up in the live snapshot per user and per port, and stopping the
// core (as every config change does) leaves the next core a clean count.
func TestLiveSnapshotFollowsRealConnectionsAcrossACoreRestart(t *testing.T) {
	for _, f := range protoFixtures() {
		t.Run(f.name, func(t *testing.T) {
			echoHost, echoPort := startEcho(t)
			p1, p2 := freeTCPPort(t), freeTCPPort(t)
			alice, bob := f.user("alice", 1), f.user("bob", 2)
			srv := newTestServer(t)
			startCore := func() {
				srv.mu.Lock()
				defer srv.mu.Unlock()
				if err := srv.startNodeLocked(startRequest{Inbounds: []inboundSpec{f.inbound("main", []uint16{p1, p2}, alice, bob)}}); err != nil {
					t.Fatalf("start: %v", err)
				}
			}
			dial := func(port uint16, u userSpec) net.Conn {
				conn, err := f.dialProxied(port, u, echoHost, echoPort)
				if err != nil {
					t.Fatalf("dial %s on %d: %v", u.Name, port, err)
				}
				t.Cleanup(func() { conn.Close() })
				if err := echoConn(conn, "presence"); err != nil {
					t.Fatalf("%s on %d: %v", u.Name, port, err)
				}
				return conn
			}
			waitFor := func(what string, ok func(liveRequest) bool) liveRequest {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for {
					req := srv.buildLiveRequest(false, 0)
					if ok(req) {
						return req
					}
					if time.Now().After(deadline) {
						t.Fatalf("never reached %q; last snapshot %+v", what, req)
					}
					time.Sleep(15 * time.Millisecond)
				}
			}

			startCore()
			a1, a2 := dial(p1, alice), dial(p1, alice)
			dial(p2, bob)
			req := waitFor("3 open", func(r liveRequest) bool { return r.ConnsTotal == 3 })
			if req.PortConns[strconv.Itoa(int(p1))] != 2 || req.PortConns[strconv.Itoa(int(p2))] != 1 || len(req.PortConns) != 2 {
				t.Errorf("port_conns = %v, want %d:2 %d:1", req.PortConns, p1, p2)
			}
			if raw, _ := json.Marshal(req.Online); string(raw) != `[["alice",2],["bob",1]]` {
				t.Errorf("online = %s", raw)
			}

			a1.Close()
			a2.Close()
			waitFor("bob only", func(r liveRequest) bool { return r.ConnsTotal == 1 && len(r.Online) == 1 })

			// A config change: the core is stopped and rebuilt. Bob's connection dies
			// with the old core; whatever became of its close signal, the new core
			// starts from zero.
			srv.mu.Lock()
			srv.stopNodeLocked()
			srv.mu.Unlock()
			if req := srv.buildLiveRequest(false, 0); req.ConnsTotal != 0 || len(req.Online) != 0 || len(req.PortConns) != 0 {
				t.Fatalf("snapshot right after the core stopped = %+v, want empty", req)
			}
			time.Sleep(150 * time.Millisecond) // late close signals of the old core
			if req := srv.buildLiveRequest(false, 0); req.ConnsTotal != 0 {
				t.Fatalf("a late close of the old core disturbed the count: %+v", req)
			}

			startCore()
			dial(p2, alice)
			req = waitFor("1 open on the new core", func(r liveRequest) bool { return r.ConnsTotal == 1 })
			if raw, _ := json.Marshal(req.Online); string(raw) != `[["alice",1]]` || req.PortConns[strconv.Itoa(int(p2))] != 1 {
				t.Errorf("new core snapshot: online %s, ports %v", raw, req.PortConns)
			}
		})
	}
}
