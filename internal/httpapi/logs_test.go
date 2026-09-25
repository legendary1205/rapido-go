package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/logstream"
)

// logSeed appends entries to a source's Redis list and returns them.
func logSeed(t *testing.T, h *Handler, source string, entries ...logstream.Entry) []logstream.Entry {
	t.Helper()
	vals := make([]interface{}, len(entries))
	for i, e := range entries {
		vals[i] = e.Encode()
	}
	if err := h.store.Cache.Raw().RPush(context.Background(), logstream.Key(source), vals...).Err(); err != nil {
		t.Fatalf("seed logs: %v", err)
	}
	return entries
}

// logEntries builds n entries with strictly increasing ids, line "line <i>".
func logEntries(n int, level func(i int) string) []logstream.Entry {
	base := time.Now().Add(-time.Hour).UnixMicro()
	out := make([]logstream.Entry, n)
	for i := range out {
		id := base + int64(i)*1000
		out[i] = logstream.Entry{ID: id, TS: logstream.TSFromID(id), Level: level(i), Line: fmt.Sprintf("line %d", i)}
	}
	return out
}

func logInfo(int) string { return "info" }

type logsBody struct {
	Entries   []logstream.Entry `json:"entries"`
	Next      int64             `json:"next"`
	Streaming bool              `json:"streaming"`
}

func logGet(t *testing.T, router http.Handler, token, query string) (int, logsBody, []byte) {
	t.Helper()
	resp := doRequest(t, router, "GET", "/api/logs?"+query, token, nil)
	var body logsBody
	_ = json.Unmarshal(resp.Raw, &body)
	return resp.Code, body, resp.Raw
}

func lineNames(entries []logstream.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Line
	}
	return out
}

func TestLogsEndpointsAreSudoOnly(t *testing.T) {
	router, sudoToken := newTestRouter(t)
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "logs-reseller", "password": "pw12345", "is_sudo": false})
	resellerToken := loginAs(t, router, "logs-reseller", "pw12345")

	for _, path := range []string{"/api/logs/sources", "/api/logs?source=panel"} {
		if resp := doRequest(t, router, "GET", path, "", nil); resp.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d, want 401", path, resp.Code)
		}
		if resp := doRequest(t, router, "GET", path, resellerToken, nil); resp.Code != http.StatusForbidden {
			t.Errorf("%s as a reseller: %d, want 403", path, resp.Code)
		}
		if resp := doRequest(t, router, "GET", path, sudoToken, nil); resp.Code != http.StatusOK {
			t.Errorf("%s as sudo: %d, want 200", path, resp.Code)
		}
	}
}

func TestLogsSourcesListPanelBackendAndEveryNode(t *testing.T) {
	router, token := newTestRouter(t)
	idA, _ := createTestNode(t, router, token, "nod1")
	idB, _ := createTestNode(t, router, token, "nod2")

	resp := doRequest(t, router, "GET", "/api/logs/sources", token, nil)
	var sources []map[string]interface{}
	if err := json.Unmarshal(resp.Raw, &sources); err != nil {
		t.Fatalf("sources is not a JSON array: %v (%s)", err, resp.Raw)
	}
	if len(sources) != 4 {
		t.Fatalf("got %d sources, want panel, backend and 2 nodes: %v", len(sources), sources)
	}
	want := []map[string]interface{}{
		{"id": "panel", "label": "Panel API", "kind": "panel"},
		{"id": "backend", "label": "Backend jobs", "kind": "backend"},
		{"id": fmt.Sprintf("node:%d", idA), "label": "nod1", "kind": "node", "status": "connecting"},
		{"id": fmt.Sprintf("node:%d", idB), "label": "nod2", "kind": "node", "status": "connecting"},
	}
	for i, w := range want {
		if len(sources[i]) != len(w) {
			t.Errorf("source %d = %v, want exactly %v", i, sources[i], w)
			continue
		}
		for k, v := range w {
			if sources[i][k] != v {
				t.Errorf("source %d field %s = %v, want %v", i, k, sources[i][k], v)
			}
		}
	}
}

func TestLogsPanelCursorLimitAndFilters(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	levels := []string{"info", "warn", "error", "debug", "info", "error", "info", "warn", "info", "error"}
	entries := logEntries(10, func(i int) string { return levels[i] })
	entries[3].Line = "line 3 upstream TIMEOUT while dialing"
	logSeed(t, h, "panel", entries...)

	// after=0: the newest `limit`, oldest first.
	code, body, raw := logGet(t, router, token, "source=panel&limit=3")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, raw)
	}
	if got := lineNames(body.Entries); strings.Join(got, "|") != "line 7|line 8|line 9" {
		t.Errorf("newest 3 = %v, want lines 7, 8, 9 oldest first", got)
	}
	if body.Next != entries[9].ID {
		t.Errorf("next = %d, want the newest id %d", body.Next, entries[9].ID)
	}
	if !body.Streaming {
		t.Error("streaming = false for the panel source, want true")
	}

	// Everything after a cursor, oldest first.
	_, body, _ = logGet(t, router, token, fmt.Sprintf("source=panel&after=%d", entries[6].ID))
	if got := lineNames(body.Entries); strings.Join(got, "|") != "line 7|line 8|line 9" {
		t.Errorf("after line 6 = %v, want lines 7, 8, 9", got)
	}

	// Cursor at the newest entry: empty list (not null), next unchanged.
	code, body, raw = logGet(t, router, token, fmt.Sprintf("source=panel&after=%d", entries[9].ID))
	if code != http.StatusOK || len(body.Entries) != 0 || body.Next != entries[9].ID {
		t.Errorf("after the newest = %d %+v, want 200, no entries, next unchanged", code, body)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Errorf("empty result serialises as %s, want \"entries\":[]", raw)
	}

	// A cursor with a small limit returns the OLDEST entries after it, and
	// following `next` walks the rest without a gap.
	_, body, _ = logGet(t, router, token, fmt.Sprintf("source=panel&after=%d&limit=4", entries[1].ID))
	if got := lineNames(body.Entries); strings.Join(got[:1], "") != "line 2" || len(got) != 4 {
		t.Errorf("first page after line 1 = %v, want 4 entries starting at line 2", got)
	}
	_, body2, _ := logGet(t, router, token, fmt.Sprintf("source=panel&after=%d&limit=100", body.Next))
	if len(body2.Entries) != 4 || body2.Entries[0].Line != "line 6" {
		t.Errorf("second page = %v, want lines 6..9", lineNames(body2.Entries))
	}

	// Minimum level.
	_, body, _ = logGet(t, router, token, "source=panel&level=warn")
	if got := lineNames(body.Entries); len(got) != 5 {
		t.Errorf("level=warn returned %v, want the 2 warn + 3 error lines", got)
	}
	for _, e := range body.Entries {
		if e.Level != "warn" && e.Level != "error" {
			t.Errorf("level=warn let a %s line through", e.Level)
		}
	}
	_, body, _ = logGet(t, router, token, "source=panel&level=ERROR")
	if len(body.Entries) != 3 {
		t.Errorf("level=ERROR returned %d entries, want 3", len(body.Entries))
	}

	// Substring, case-insensitive; combined with a level and a limit.
	_, body, _ = logGet(t, router, token, "source=panel&q=timeout")
	if len(body.Entries) != 1 || !strings.Contains(body.Entries[0].Line, "TIMEOUT") {
		t.Errorf("q=timeout = %v, want the one TIMEOUT line", lineNames(body.Entries))
	}
	_, body, _ = logGet(t, router, token, "source=panel&q=line&level=error&limit=2")
	if got := lineNames(body.Entries); strings.Join(got, "|") != "line 5|line 9" {
		t.Errorf("q=line&level=error&limit=2 = %v, want the newest two errors (5, 9)", got)
	}
	_, body, _ = logGet(t, router, token, fmt.Sprintf("source=panel&q=nomatch&after=%d", entries[2].ID))
	if len(body.Entries) != 0 || body.Next != entries[2].ID {
		t.Errorf("no match: %+v, want no entries and next == after", body)
	}
}

func TestLogsLimitDefaultsAndClamps(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	logSeed(t, h, "panel", logEntries(1200, logInfo)...)

	_, body, _ := logGet(t, router, token, "source=panel")
	if len(body.Entries) != 300 {
		t.Errorf("default limit returned %d entries, want 300", len(body.Entries))
	}
	if body.Entries[299].Line != "line 1199" {
		t.Errorf("default page ends at %q, want the newest line", body.Entries[299].Line)
	}
	_, body, _ = logGet(t, router, token, "source=panel&limit=5000")
	if len(body.Entries) != 1000 {
		t.Errorf("limit=5000 returned %d entries, want the 1000 maximum", len(body.Entries))
	}
	_, body, _ = logGet(t, router, token, "source=panel&limit=0")
	if len(body.Entries) != 300 {
		t.Errorf("limit=0 returned %d entries, want the default 300", len(body.Entries))
	}
}

func TestLogsCursorReachesBackBeyondTheTail(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	entries := logSeed(t, h, "panel", logEntries(900, logInfo)...)

	// 800 entries are newer than the cursor but the page is 300: the handler
	// must look further back than the newest 300 to find the entries right
	// after the cursor, not skip to the tail.
	_, body, _ := logGet(t, router, token, fmt.Sprintf("source=panel&after=%d&limit=300", entries[99].ID))
	if len(body.Entries) != 300 || body.Entries[0].Line != "line 100" || body.Entries[299].Line != "line 399" {
		t.Fatalf("page after line 99 = %d entries %q .. %q, want 300 entries line 100 .. line 399",
			len(body.Entries), body.Entries[0].Line, body.Entries[len(body.Entries)-1].Line)
	}
	if body.Next != entries[399].ID {
		t.Errorf("next = %d, want the id of line 399", body.Next)
	}
}

func TestLogsInterleavedPublishersComeBackSorted(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	e := logEntries(4, logInfo)
	// Two panel replicas pushing batches in turn: not id-ordered in the list.
	logSeed(t, h, "panel", e[0], e[2])
	logSeed(t, h, "panel", e[1], e[3])
	_, body, _ := logGet(t, router, token, "source=panel")
	if got := lineNames(body.Entries); strings.Join(got, "|") != "line 0|line 1|line 2|line 3" {
		t.Errorf("entries = %v, want them sorted by id", got)
	}
}

func TestLogsBadParametersAndUnknownSources(t *testing.T) {
	router, token := newTestRouter(t)
	for query, want := range map[string]int{
		"":                               http.StatusUnprocessableEntity,
		"source=":                        http.StatusUnprocessableEntity,
		"source=panel&after=abc":         http.StatusUnprocessableEntity,
		"source=panel&after=-5":          http.StatusUnprocessableEntity,
		"source=panel&limit=lots":        http.StatusUnprocessableEntity,
		"source=panel&level=loud":        http.StatusUnprocessableEntity,
		"source=nginx":                   http.StatusNotFound,
		"source=node:999":                http.StatusNotFound,
		"source=node:abc":                http.StatusNotFound,
		"source=node:":                   http.StatusNotFound,
		"source=node:0":                  http.StatusNotFound,
		"source=logs:panel":              http.StatusNotFound,
		"source=panel&level=all&limit=1": http.StatusOK,
	} {
		if code, _, raw := logGet(t, router, token, query); code != want {
			t.Errorf("?%s: status %d, want %d (%s)", query, code, want, raw)
		}
	}
}

func TestLogsNodeSourceSetsTheWatchKeyAndReportsStreaming(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	nodeID, secret := createTestNode(t, router, token, "logs-node")
	rdb := h.store.Cache.Raw()
	ctx := context.Background()
	source := logstream.NodeSource(nodeID)
	entries := logSeed(t, h, source, logEntries(3, logInfo)...)

	if n := rdb.Exists(ctx, logstream.WatchKey(nodeID)).Val(); n != 0 {
		t.Fatal("watch key exists before anyone looked")
	}
	code, body, raw := logGet(t, router, token, "source="+source)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, raw)
	}
	if len(body.Entries) != 3 || body.Next != entries[2].ID {
		t.Errorf("entries = %v next %d, want the 3 stored lines", lineNames(body.Entries), body.Next)
	}
	if body.Streaming {
		t.Error("streaming = true although the node never sent a node-live")
	}
	if ttl := rdb.TTL(ctx, logstream.WatchKey(nodeID)).Val(); ttl <= 0 || ttl > 20*time.Second {
		t.Errorf("watch key TTL = %v, want within (0, 20s]", ttl)
	}

	// The node's next node-live is told to stream, and once it has sent one the
	// panel reports the node as streaming.
	resp := nlPost(t, router, secret, []byte(`{"conns_total":1}`), false)
	if resp.Body["log_stream"] != true || resp.Body["interval_ms"] != float64(1000) {
		t.Errorf("node-live response = %v, want it to ask for streaming", resp.Body)
	}
	if _, body, _ = logGet(t, router, token, "source="+source); !body.Streaming {
		t.Error("streaming = false right after a node-live, want true")
	}

	// The watch expires on its own when the page stops polling; polling again
	// re-arms it.
	rdb.Del(ctx, logstream.WatchKey(nodeID))
	logGet(t, router, token, "source="+source)
	if n := rdb.Exists(ctx, logstream.WatchKey(nodeID)).Val(); n != 1 {
		t.Error("polling did not re-arm the watch key")
	}

	// The panel and backend sources do not touch node watch keys.
	logGet(t, router, token, "source=panel")
	if n := rdb.Exists(ctx, logstream.WatchKey(0)).Val(); n != 0 {
		t.Error("a panel read created a watch key")
	}
}

func TestLogsRedisDownFallsBackToTheProcessRingForItsOwnSource(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	nodeID, _ := createTestNode(t, router, token, "logs-down-node")

	// Lines this process "logged", including an error with a marker.
	marker := fmt.Sprintf("ringmark-%d", time.Now().UnixNano())
	ring := logstream.ProcessRing()
	ring.Add(time.Time{}, "info", `{"msg":"`+marker+` first"}`)
	ring.Add(time.Time{}, "error", `{"msg":"`+marker+` second"}`)
	newest := ring.Add(time.Time{}, "info", `{"msg":"`+marker+` third"}`)

	dead := cache.New("127.0.0.1:1", "", 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer dead.Close()
	h.store.Cache = dead

	code, body, raw := logGet(t, router, token, "source="+logstream.ProcessSource()+"&q="+marker)
	if code != http.StatusOK {
		t.Fatalf("panel source with Redis down: %d %s, want 200 from the ring", code, raw)
	}
	if len(body.Entries) != 3 || body.Next != newest.ID || !body.Streaming {
		t.Errorf("fallback = %d entries next %d streaming %v, want the 3 ring lines, next %d, streaming true", len(body.Entries), body.Next, body.Streaming, newest.ID)
	}
	if body.Entries[0].ID >= body.Entries[1].ID {
		t.Errorf("fallback entries are not oldest-first")
	}
	// Filters and the cursor work on the ring too (each Redis-down request is
	// slow - every Redis call retries before failing - so this is one request).
	_, body, _ = logGet(t, router, token, fmt.Sprintf("source=%s&level=error&q=%s&after=%d", logstream.ProcessSource(), marker, body.Entries[0].ID))
	if len(body.Entries) != 1 || !strings.Contains(body.Entries[0].Line, "second") {
		t.Errorf("level=error after the first line, fallback = %v, want just the error line", lineNames(body.Entries))
	}

	// Everything the ring cannot speak for is a 503.
	for _, source := range []string{"backend", fmt.Sprintf("node:%d", nodeID)} {
		if code, _, raw := logGet(t, router, token, "source="+source); code != http.StatusServiceUnavailable {
			t.Errorf("source %s with Redis down: %d %s, want 503", source, code, raw)
		}
	}
}

func TestPickLogPageCutsTheRightEnd(t *testing.T) {
	entries := logEntries(10, func(i int) string {
		if i%2 == 0 {
			return "info"
		}
		return "error"
	})
	names := func(es []logstream.Entry) string { return strings.Join(lineNames(es), "|") }

	if got := names(pickLogPage(entries, 0, 3, 0, "")); got != "line 7|line 8|line 9" {
		t.Errorf("no cursor keeps the newest: %s", got)
	}
	if got := names(pickLogPage(entries, entries[4].ID, 3, 0, "")); got != "line 5|line 6|line 7" {
		t.Errorf("a cursor keeps the oldest after it: %s", got)
	}
	errRank, _ := logstream.LevelRank("error")
	if got := names(pickLogPage(entries, 0, 2, errRank, "")); got != "line 7|line 9" {
		t.Errorf("level filter is applied before the cut: %s", got)
	}
	if got := names(pickLogPage(entries, 0, 5, 0, "line 3")); got != "line 3" {
		t.Errorf("substring filter: %s", got)
	}
	if got := pickLogPage(nil, 0, 5, 0, ""); got == nil || len(got) != 0 {
		t.Errorf("empty input must give an empty non-nil slice, got %#v", got)
	}
	if nextLogID(nil, 42) != 42 || nextLogID(entries, 0) != entries[9].ID {
		t.Error("nextLogID: keep the cursor when empty, else the newest id")
	}
}

// The live-log polls and the node channel run every second or few; a
// successful call must not be access-logged (it would fill the log stream with
// lines about reading it), a failed one must still be.
func TestPollingEndpointsAreNotAccessLoggedUnlessTheyFail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r := gin.New()
	r.Use(slogMiddleware(logger), apiClientLogMiddleware(logger))
	fail := false
	handler := func(c *gin.Context) {
		if fail {
			c.JSON(http.StatusServiceUnavailable, gin.H{"detail": "down"})
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	}
	r.POST("/api/internal/node-live", handler)
	r.GET("/api/logs", handler)
	r.GET("/api/logs/sources", handler)
	r.GET("/api/system", handler)

	call := func(method, path string) {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("User-Agent", "Go-http-client/2.0") // a non-browser client, so apiclient would log it
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	for _, c := range [][2]string{{"POST", "/api/internal/node-live"}, {"GET", "/api/logs"}, {"GET", "/api/logs/sources"}} {
		buf.Reset()
		call(c[0], c[1])
		if buf.Len() != 0 {
			t.Errorf("%s %s succeeded but was logged:\n%s", c[0], c[1], buf.String())
		}
	}

	fail = true
	for _, c := range [][2]string{{"POST", "/api/internal/node-live"}, {"GET", "/api/logs"}} {
		buf.Reset()
		call(c[0], c[1])
		if !strings.Contains(buf.String(), c[1]) || !strings.Contains(buf.String(), "503") {
			t.Errorf("%s %s failed but was not logged:\n%s", c[0], c[1], buf.String())
		}
	}

	// Any other endpoint keeps its access line.
	fail = false
	buf.Reset()
	call("GET", "/api/system")
	if !strings.Contains(buf.String(), "/api/system") {
		t.Errorf("an ordinary endpoint lost its access log:\n%s", buf.String())
	}
}
