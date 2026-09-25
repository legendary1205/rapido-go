package httpapi

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/legendary1205/rapido-go/internal/logstream"
)

const (
	// nodeLiveMaxBody caps both the bytes on the wire and the decoded JSON, so
	// a gzip bomb costs no more than a plain oversized body.
	nodeLiveMaxBody = 4 << 20

	nodeLiveIdleIntervalMS   = 5000
	nodeLiveStreamIntervalMS = 1000

	// presenceZAddChunk is how many usernames go into one ZADD command.
	presenceZAddChunk = 1000

	// nodeLogTTL bounds how long a node's stored log list lives after its last
	// new line, so a deleted node does not leave a list behind forever.
	nodeLogTTL = 7 * 24 * time.Hour

	// logPushChunk caps how many entries go into one RPUSH command.
	logPushChunk = 500
)

type nodeLiveLog struct {
	ID    int64  `json:"id"`
	TS    string `json:"ts"`
	Level string `json:"level"`
	Line  string `json:"line"`
}

// nodeLiveRequest is the body of POST /api/internal/node-live. `suppressed`
// (counters of dropped benign noise) is informational and deliberately not
// decoded.
type nodeLiveRequest struct {
	ConnsTotal int64            `json:"conns_total"`
	PortConns  map[string]int64 `json:"port_conns"`
	// Online is a full snapshot of [username, open connections] pairs.
	Online [][]any       `json:"online"`
	Logs   []nodeLiveLog `json:"logs"`
}

type nodeLiveResponse struct {
	LogStream  bool  `json:"log_stream"`
	LogAfter   int64 `json:"log_after"`
	IntervalMS int   `json:"interval_ms"`
}

// handleNodeLive implements POST /api/internal/node-live - the node's live
// channel, sent every 5 s (every second while someone is watching that node's
// logs). It carries who is connected right now and per-port connection counts,
// which become the Redis presence keys (see presence.go), plus, on request, the
// node's log lines.
//
// It touches no database - it runs every 5 s per node with a few thousand
// usernames in the body - and only Redis, so any panel process can take it,
// unlike node-report which has to reach the backend singleton.
func (h *Handler) handleNodeLive(c *gin.Context) {
	nodeID := c.MustGet(nodeIDContextKey).(int32)

	req, status, err := readNodeLiveRequest(c)
	if err != nil {
		c.JSON(status, gin.H{"detail": err.Error()})
		return
	}
	nowMS := time.Now().UnixMilli()

	members := make([]redis.Z, 0, len(req.Online))
	for i, row := range req.Online {
		name, ok := row[0].(string)
		if !ok {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "online[" + strconv.Itoa(i) + "]: username must be a string"})
			return
		}
		if len(row) > 1 {
			n, ok := row[1].(float64)
			if !ok {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "online[" + strconv.Itoa(i) + "]: connection count must be a number"})
				return
			}
			if n <= 0 {
				continue // not actually connected
			}
		}
		if name == "" {
			continue
		}
		members = append(members, redis.Z{Score: float64(nowMS), Member: name})
	}

	ports := make([]any, 0, 2*len(req.PortConns))
	var portSum int64
	for port, n := range req.PortConns {
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 || n < 1 {
			continue
		}
		ports = append(ports, port, n)
		portSum += n
	}
	total := req.ConnsTotal
	if total <= 0 {
		total = portSum
	}

	ctx := c.Request.Context()
	rdb := h.store.Cache.Raw()
	seqKey := logstream.NodeSeqKey(nodeID)

	// Lines the panel already stored are skipped, so the node can safely resend
	// after a lost response. That needs the stored cursor first, which costs one
	// extra round trip - paid only by requests that actually carry lines.
	var newLogs []logstream.Entry
	if len(req.Logs) > 0 {
		stored, err := rdb.Get(ctx, seqKey).Int64()
		if err != nil && !errors.Is(err, redis.Nil) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"detail": "Presence store (Redis) unavailable: " + err.Error()})
			return
		}
		newLogs = prepareNodeLogs(req.Logs, stored)
	}

	// Everything else - presence, the ports replacement (DEL + HSET must not be
	// observable half-done) and the log append - is one transaction, and the
	// same round trip reads back what the response needs.
	pipe := rdb.TxPipeline()
	for start := 0; start < len(members); start += presenceZAddChunk {
		pipe.ZAdd(ctx, presenceUsersKey, members[start:min(start+presenceZAddChunk, len(members))]...)
	}
	portsKey := presenceNodePortsKey(nodeID)
	pipe.Del(ctx, portsKey)
	if len(ports) > 0 {
		pipe.HSet(ctx, portsKey, ports...)
		pipe.Expire(ctx, portsKey, presenceKeyTTL)
	}
	pipe.Set(ctx, presenceNodeTotalKey(nodeID), total, presenceKeyTTL)
	pipe.Set(ctx, presenceLiveKey, "1", presenceKeyTTL)
	if len(newLogs) > 0 {
		listKey := logstream.Key(logstream.NodeSource(nodeID))
		for start := 0; start < len(newLogs); start += logPushChunk {
			vals := make([]any, 0, logPushChunk)
			for _, e := range newLogs[start:min(start+logPushChunk, len(newLogs))] {
				vals = append(vals, e.Encode())
			}
			pipe.RPush(ctx, listKey, vals...)
		}
		pipe.LTrim(ctx, listKey, -logstream.RingSize, -1)
		pipe.Expire(ctx, listKey, nodeLogTTL)
		pipe.Set(ctx, seqKey, newLogs[len(newLogs)-1].ID, nodeLogTTL)
	}
	seq := pipe.Get(ctx, seqKey)
	watched := pipe.Exists(ctx, logstream.WatchKey(nodeID))

	cmds, execErr := pipe.Exec(ctx)
	failed := execErr != nil && !errors.Is(execErr, redis.Nil)
	for _, cmd := range cmds {
		if e := cmd.Err(); e != nil && !errors.Is(e, redis.Nil) {
			failed = true
		}
	}
	if failed {
		msg := "Presence store (Redis) unavailable"
		if execErr != nil {
			msg += ": " + execErr.Error()
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"detail": msg})
		return
	}

	resp := nodeLiveResponse{IntervalMS: nodeLiveIdleIntervalMS}
	if after, err := strconv.ParseInt(seq.Val(), 10, 64); err == nil {
		resp.LogAfter = after
	}
	if watched.Val() > 0 {
		resp.LogStream = true
		resp.IntervalMS = nodeLiveStreamIntervalMS
	}
	c.JSON(http.StatusOK, resp)
}

// readNodeLiveRequest reads and decodes the body, plain or gzip, capping the
// decoded size. The returned status is only meaningful with a non-nil error.
func readNodeLiveRequest(c *gin.Context) (*nodeLiveRequest, int, error) {
	var reader io.Reader = http.MaxBytesReader(c.Writer, c.Request.Body, nodeLiveMaxBody)
	if strings.EqualFold(strings.TrimSpace(c.GetHeader("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, http.StatusUnprocessableEntity, errors.New("invalid gzip body: " + err.Error())
		}
		defer gz.Close()
		reader = gz
	}
	raw, err := io.ReadAll(io.LimitReader(reader, nodeLiveMaxBody+1))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig) || len(raw) > nodeLiveMaxBody:
		return nil, http.StatusRequestEntityTooLarge, errors.New("body exceeds 4 MiB")
	case err != nil:
		return nil, http.StatusUnprocessableEntity, errors.New("could not read body: " + err.Error())
	}

	var req nodeLiveRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, http.StatusUnprocessableEntity, errors.New("invalid JSON: " + err.Error())
	}
	for i, row := range req.Online {
		if len(row) == 0 {
			return nil, http.StatusUnprocessableEntity, errors.New("online[" + strconv.Itoa(i) + "] is empty, want [username, connections]")
		}
	}
	return &req, 0, nil
}

// prepareNodeLogs turns a node's batch into the entries to store: only lines
// newer than the stored cursor, in id order, each id once, at most one list's
// worth (the newest), with the level, timestamp and size normalised.
func prepareNodeLogs(in []nodeLiveLog, stored int64) []logstream.Entry {
	out := make([]logstream.Entry, 0, len(in))
	for _, l := range in {
		if l.ID <= stored || l.ID <= 0 {
			continue
		}
		line := l.Line
		if len(line) > logstream.MaxLineBytes {
			line = strings.ToValidUTF8(line[:logstream.MaxLineBytes], "") + "..."
		}
		ts := l.TS
		if ts == "" {
			ts = logstream.TSFromID(l.ID)
		}
		out = append(out, logstream.Entry{ID: l.ID, TS: ts, Level: logstream.NormalizeLevel(l.Level), Line: line})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	deduped := out[:0]
	for _, e := range out {
		if n := len(deduped); n > 0 && deduped[n-1].ID == e.ID {
			continue
		}
		deduped = append(deduped, e)
	}
	if len(deduped) > logstream.RingSize {
		deduped = deduped[len(deduped)-logstream.RingSize:]
	}
	return deduped
}
