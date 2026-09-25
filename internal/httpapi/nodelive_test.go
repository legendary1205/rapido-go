package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/logstream"
)

// nlPost sends a node-live request. gz gzips the body and sets Content-Encoding.
func nlPost(t *testing.T, router http.Handler, secret string, body []byte, gz bool) apiResponse {
	t.Helper()
	if gz {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		body = buf.Bytes()
	}
	req := httptest.NewRequest("POST", "/api/internal/node-live", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if gz {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var decoded map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return apiResponse{Code: rec.Code, Body: decoded, Raw: rec.Body.Bytes()}
}

func nlJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func nlLog(id int64, level, line string) map[string]interface{} {
	return map[string]interface{}{"id": id, "ts": time.UnixMicro(id).UTC().Format(time.RFC3339Nano), "level": level, "line": line}
}

// nlStored reads back a node's stored log list.
func nlStored(t *testing.T, h *Handler, nodeID int32) []logstream.Entry {
	t.Helper()
	raw, err := h.store.Cache.Raw().LRange(context.Background(), logstream.Key(logstream.NodeSource(nodeID)), 0, -1).Result()
	if err != nil {
		t.Fatalf("read node log list: %v", err)
	}
	out := make([]logstream.Entry, 0, len(raw))
	for _, r := range raw {
		e, ok := logstream.Decode(r)
		if !ok {
			t.Fatalf("stored element is not a log entry: %s", r)
		}
		out = append(out, e)
	}
	return out
}

func TestNodeLiveRejectsMissingOrInvalidSecret(t *testing.T) {
	router, _ := newTestRouter(t)
	if resp := nlPost(t, router, "", []byte(`{}`), false); resp.Code != http.StatusUnauthorized {
		t.Errorf("missing secret: %d, want 401", resp.Code)
	}
	if resp := nlPost(t, router, "not-a-real-secret", []byte(`{}`), false); resp.Code != http.StatusUnauthorized {
		t.Errorf("invalid secret: %d, want 401", resp.Code)
	}
}

func TestNodeLiveWritesPresenceKeysPlainAndGzip(t *testing.T) {
	for _, gz := range []bool{false, true} {
		t.Run(fmt.Sprintf("gzip=%v", gz), func(t *testing.T) {
			router, token, h := newTestRouterAndHandler(t)
			nodeID, secret := createTestNode(t, router, token, "live-node")
			ctx := context.Background()
			rdb := h.store.Cache.Raw()

			before := time.Now().UnixMilli()
			resp := nlPost(t, router, secret, nlJSON(t, map[string]interface{}{
				"conns_total": 3,
				"port_conns":  map[string]int{"20000": 2, "20001": 1},
				"online":      [][]interface{}{{"alice", 2}, {"bob", 1}},
				"suppressed":  map[string]int{"client_eof": 5787},
			}), gz)
			if resp.Code != http.StatusOK {
				t.Fatalf("node-live: %d %v", resp.Code, resp.Body)
			}
			if resp.Body["log_stream"] != false || resp.Body["log_after"] != float64(0) || resp.Body["interval_ms"] != float64(5000) {
				t.Errorf("response = %v, want {log_stream:false, log_after:0, interval_ms:5000}", resp.Body)
			}
			if len(resp.Body) != 3 {
				t.Errorf("response has %d fields, want exactly log_stream/log_after/interval_ms: %v", len(resp.Body), resp.Body)
			}

			for _, name := range []string{"alice", "bob"} {
				score, err := rdb.ZScore(ctx, presenceUsersKey, name).Result()
				if err != nil {
					t.Fatalf("ZSCORE %s: %v", name, err)
				}
				if int64(score) < before || int64(score) > time.Now().UnixMilli() {
					t.Errorf("score of %s = %v, want the current time in ms (>= %d)", name, int64(score), before)
				}
			}
			if v := rdb.Get(ctx, presenceLiveKey).Val(); v != "1" {
				t.Errorf("presence:live = %q, want \"1\"", v)
			}
			ports := rdb.HGetAll(ctx, presenceNodePortsKey(nodeID)).Val()
			if len(ports) != 2 || ports["20000"] != "2" || ports["20001"] != "1" {
				t.Errorf("ports hash = %v, want {20000:2, 20001:1}", ports)
			}
			if v := rdb.Get(ctx, presenceNodeTotalKey(nodeID)).Val(); v != "3" {
				t.Errorf("total = %q, want \"3\"", v)
			}
			for _, key := range []string{presenceLiveKey, presenceNodePortsKey(nodeID), presenceNodeTotalKey(nodeID)} {
				if ttl := rdb.TTL(ctx, key).Val(); ttl <= 0 || ttl > 20*time.Second {
					t.Errorf("TTL of %s = %v, want within (0, 20s]", key, ttl)
				}
			}

			// The ports hash is REPLACED, not merged.
			nlPost(t, router, secret, nlJSON(t, map[string]interface{}{
				"conns_total": 5, "port_conns": map[string]int{"20002": 5}, "online": [][]interface{}{{"alice", 5}},
			}), gz)
			ports = rdb.HGetAll(ctx, presenceNodePortsKey(nodeID)).Val()
			if len(ports) != 1 || ports["20002"] != "5" {
				t.Errorf("ports hash after the second report = %v, want only {20002:5}", ports)
			}
			// No open connections at all: the hash goes away, the total stays (0).
			nlPost(t, router, secret, nlJSON(t, map[string]interface{}{"conns_total": 0, "port_conns": map[string]int{}, "online": [][]interface{}{}}), gz)
			if n := rdb.Exists(ctx, presenceNodePortsKey(nodeID)).Val(); n != 0 {
				t.Errorf("ports hash still exists after a report with no ports")
			}
			if v := rdb.Get(ctx, presenceNodeTotalKey(nodeID)).Val(); v != "0" {
				t.Errorf("total = %q after an empty report, want \"0\"", v)
			}
		})
	}
}

func TestNodeLiveIgnoresJunkAndKeepsPerNodeKeysSeparate(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	idA, secretA := createTestNode(t, router, token, "live-a")
	idB, secretB := createTestNode(t, router, token, "live-b")
	rdb := h.store.Cache.Raw()
	ctx := context.Background()

	nlPost(t, router, secretA, nlJSON(t, map[string]interface{}{
		"port_conns": map[string]int{"20000": 4, "notaport": 9, "70000": 1, "20009": 0},
		"online":     [][]interface{}{{"carol", 1}, {"", 1}, {"ghost", 0}},
	}), false)
	nlPost(t, router, secretB, nlJSON(t, map[string]interface{}{"conns_total": 7, "port_conns": map[string]int{"30000": 7}}), false)

	if ports := rdb.HGetAll(ctx, presenceNodePortsKey(idA)).Val(); len(ports) != 1 || ports["20000"] != "4" {
		t.Errorf("node A ports = %v, want only the valid {20000:4}", ports)
	}
	// conns_total omitted: falls back to the sum of the (valid) ports.
	if v := rdb.Get(ctx, presenceNodeTotalKey(idA)).Val(); v != "4" {
		t.Errorf("node A total = %q, want \"4\"", v)
	}
	if ports := rdb.HGetAll(ctx, presenceNodePortsKey(idB)).Val(); len(ports) != 1 || ports["30000"] != "7" {
		t.Errorf("node B ports = %v, want {30000:7}", ports)
	}
	if n := rdb.ZCard(ctx, presenceUsersKey).Val(); n != 1 {
		t.Errorf("presence:users has %d members, want 1 (only carol; empty names and 0-connection entries are skipped)", n)
	}
}

func TestNodeLiveHandlesManyUsersInChunks(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	_, secret := createTestNode(t, router, token, "live-big")

	online := make([][]interface{}, 0, 2500)
	for i := 0; i < 2500; i++ {
		online = append(online, []interface{}{"bulk_user_" + strconv.Itoa(i), 1})
	}
	resp := nlPost(t, router, secret, nlJSON(t, map[string]interface{}{"conns_total": 2500, "online": online}), true)
	if resp.Code != http.StatusOK {
		t.Fatalf("node-live: %d %v", resp.Code, resp.Body)
	}
	if n := h.store.Cache.Raw().ZCard(context.Background(), presenceUsersKey).Val(); n != 2500 {
		t.Errorf("presence:users has %d members, want 2500", n)
	}
}

func TestNodeLiveMalformedBodiesAre422(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "live-bad")

	for name, body := range map[string]string{
		"empty body":               ``,
		"not json":                 `not json`,
		"array instead of object":  `[]`,
		"online is not a list":     `{"online":"alice"}`,
		"online row is not a list": `{"online":["alice"]}`,
		"online row is empty":      `{"online":[[]]}`,
		"username is not a string": `{"online":[[5,1]]}`,
		"count is not a number":    `{"online":[["alice","many"]]}`,
		"port_conns wrong type":    `{"port_conns":[1,2]}`,
		"logs wrong type":          `{"logs":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := nlPost(t, router, secret, []byte(body), false)
			if resp.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s: status %d, want 422 (body %s)", name, resp.Code, resp.Raw)
			}
			if d, _ := resp.Body["detail"].(string); d == "" {
				t.Errorf("%s: no detail in the 422 response: %s", name, resp.Raw)
			}
		})
	}

	// Content-Encoding: gzip on a body that is not gzip.
	req := httptest.NewRequest("POST", "/api/internal/node-live", strings.NewReader(`{"online":[]}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("fake gzip: status %d, want 422", rec.Code)
	}
}

func TestNodeLiveCapsDecodedBodyAt4MiB(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "live-huge")

	huge := []byte(`{"logs":[{"id":1,"line":"` + strings.Repeat("a", 5<<20) + `"}]}`)
	// The gzip body is a few KB on the wire but 5 MiB once decoded.
	if resp := nlPost(t, router, secret, huge, true); resp.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("gzip bomb: status %d, want 413", resp.Code)
	}
	if resp := nlPost(t, router, secret, huge, false); resp.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("plain oversized body: status %d, want 413", resp.Code)
	}
	// Just under the cap still goes through.
	ok := []byte(`{"online":[["alice",1]],"pad":"` + strings.Repeat("a", 3<<20) + `"}`)
	if resp := nlPost(t, router, secret, ok, true); resp.Code != http.StatusOK {
		t.Errorf("3 MiB decoded body: status %d, want 200", resp.Code)
	}
}

func TestNodeLiveIngestsLogsInOrderWithoutDuplicates(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	nodeID, secret := createTestNode(t, router, token, "live-logs")
	rdb := h.store.Cache.Raw()
	ctx := context.Background()

	// Nobody is watching: the node is told not to stream.
	resp := nlPost(t, router, secret, []byte(`{}`), false)
	if resp.Body["log_stream"] != false || resp.Body["interval_ms"] != float64(5000) || resp.Body["log_after"] != float64(0) {
		t.Fatalf("idle response = %v", resp.Body)
	}

	// Someone opens the node's logs: the watch key appears.
	if err := rdb.Set(ctx, logstream.WatchKey(nodeID), "1", 20*time.Second).Err(); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UnixMicro()
	// Out of order, with a duplicate id, an odd level spelling and a missing ts.
	resp = nlPost(t, router, secret, nlJSON(t, map[string]interface{}{"logs": []interface{}{
		nlLog(base+2, "info", "second"),
		nlLog(base+1, "warning", "first"),
		nlLog(base+3, "error", "third"),
		nlLog(base+3, "error", "third again"),
		map[string]interface{}{"id": base + 4, "level": "debug", "line": "no ts"},
	}}), true)
	if resp.Code != http.StatusOK {
		t.Fatalf("node-live: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["log_stream"] != true || resp.Body["interval_ms"] != float64(1000) {
		t.Errorf("response while watched = %v, want log_stream:true interval_ms:1000", resp.Body)
	}
	if got := int64(resp.Body["log_after"].(float64)); got != base+4 {
		t.Errorf("log_after = %d, want the newest stored id %d", got, base+4)
	}

	stored := nlStored(t, h, nodeID)
	wantLines := []string{"first", "second", "third", "no ts"}
	if len(stored) != len(wantLines) {
		t.Fatalf("stored %d entries, want %d: %+v", len(stored), len(wantLines), stored)
	}
	for i, want := range wantLines {
		if stored[i].Line != want {
			t.Errorf("stored[%d].line = %q, want %q (id order)", i, stored[i].Line, want)
		}
	}
	if stored[0].Level != "warn" {
		t.Errorf("level %q not normalised to warn", stored[0].Level)
	}
	if stored[3].TS == "" || stored[3].Level != "debug" {
		t.Errorf("entry without ts = %+v, want a generated ts and level debug", stored[3])
	}
	if v, _ := rdb.Get(ctx, logstream.NodeSeqKey(nodeID)).Int64(); v != base+4 {
		t.Errorf("logs:nodeseq = %d, want %d", v, base+4)
	}
	// Exactly the four documented fields per element.
	raw := rdb.LIndex(ctx, logstream.Key(logstream.NodeSource(nodeID)), 0).Val()
	var elem map[string]interface{}
	_ = json.Unmarshal([]byte(raw), &elem)
	if len(elem) != 4 || elem["id"] == nil || elem["ts"] == nil || elem["level"] == nil || elem["line"] == nil {
		t.Errorf("stored element = %s, want exactly id/ts/level/line", raw)
	}

	// A resend of what was already stored (a lost response) plus one new line:
	// only the new line lands.
	resp = nlPost(t, router, secret, nlJSON(t, map[string]interface{}{"logs": []interface{}{
		nlLog(base+3, "error", "third"),
		nlLog(base+4, "debug", "no ts"),
		nlLog(base+5, "info", "fifth"),
	}}), false)
	if got := int64(resp.Body["log_after"].(float64)); got != base+5 {
		t.Errorf("log_after after the resend = %d, want %d", got, base+5)
	}
	stored = nlStored(t, h, nodeID)
	if len(stored) != 5 || stored[4].Line != "fifth" {
		t.Errorf("after the resend the list is %+v, want the 4 old entries plus 'fifth' once", stored)
	}

	// Only old lines: nothing is added and the cursor does not move back.
	resp = nlPost(t, router, secret, nlJSON(t, map[string]interface{}{"logs": []interface{}{nlLog(base+1, "info", "ancient")}}), false)
	if got := int64(resp.Body["log_after"].(float64)); got != base+5 {
		t.Errorf("log_after = %d after an all-old batch, want it to stay %d", got, base+5)
	}
	if n := len(nlStored(t, h, nodeID)); n != 5 {
		t.Errorf("list grew to %d entries on an all-old batch", n)
	}

	// Another node's stream is independent.
	otherID, otherSecret := createTestNode(t, router, token, "live-logs-2")
	resp = nlPost(t, router, otherSecret, nlJSON(t, map[string]interface{}{"logs": []interface{}{nlLog(base+1, "info", "other node")}}), false)
	if got := int64(resp.Body["log_after"].(float64)); got != base+1 {
		t.Errorf("second node log_after = %d, want %d", got, base+1)
	}
	if n := len(nlStored(t, h, otherID)); n != 1 {
		t.Errorf("second node stored %d entries, want 1", n)
	}
	if resp.Body["log_stream"] != false {
		t.Errorf("second node is not watched but was told to stream: %v", resp.Body)
	}
}

func TestNodeLiveTrimsStoredLogsToNewest2000(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	nodeID, secret := createTestNode(t, router, token, "live-trim")

	base := time.Now().UnixMicro()
	for batch := 0; batch < 3; batch++ {
		lines := make([]interface{}, 0, 700)
		for i := 0; i < 700; i++ {
			n := batch*700 + i
			lines = append(lines, nlLog(base+int64(n)+1, "info", fmt.Sprintf("line %d", n)))
		}
		if resp := nlPost(t, router, secret, nlJSON(t, map[string]interface{}{"logs": lines}), true); resp.Code != http.StatusOK {
			t.Fatalf("batch %d: %d %v", batch, resp.Code, resp.Body)
		}
	}
	stored := nlStored(t, h, nodeID)
	if len(stored) != 2000 {
		t.Fatalf("stored %d entries after 2100 lines, want 2000", len(stored))
	}
	if stored[0].Line != "line 100" || stored[1999].Line != "line 2099" {
		t.Errorf("kept lines %q .. %q, want line 100 .. line 2099 (the newest 2000)", stored[0].Line, stored[1999].Line)
	}
}

func TestNodeLiveRedisDownAnswers503(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	_, secret := createTestNode(t, router, token, "live-down")

	// Nothing listens on port 1.
	dead := cache.New("127.0.0.1:1", "", 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer dead.Close()
	h.store.Cache = dead

	resp := nlPost(t, router, secret, nlJSON(t, map[string]interface{}{"online": [][]interface{}{{"alice", 1}}}), false)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 (body %s)", resp.Code, resp.Raw)
	}
	if d, _ := resp.Body["detail"].(string); !strings.Contains(d, "Redis") {
		t.Errorf("detail = %q, want it to say Redis is the problem", d)
	}
	// With logs too (they need a Redis read first).
	resp = nlPost(t, router, secret, nlJSON(t, map[string]interface{}{"logs": []interface{}{nlLog(time.Now().UnixMicro(), "info", "x")}}), false)
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("with logs: status %d, want 503", resp.Code)
	}
}
