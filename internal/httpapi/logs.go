package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/legendary1205/rapido-go/internal/logstream"
)

const (
	logSourcePanel   = "panel"
	logSourceBackend = "backend"
	logSourceNodePfx = "node:"

	defaultLogLimit = 300
	maxLogLimit     = 1000

	// logWatchTTL is how long one poll keeps a node streaming: the node sees
	// the watch key on its next node-live (within 5 s) and streams for as long
	// as it keeps being refreshed.
	logWatchTTL = 20 * time.Second
)

// quietWhenOK reports the paths whose successful requests are not access-logged:
// the live-log polls and the node channel run every second or few, and a line
// per call would fill the very log stream being watched with lines about
// watching it. Failures are still logged.
func quietWhenOK(path string) bool {
	switch path {
	case "/api/logs", "/api/logs/sources", "/api/internal/node-live":
		return true
	}
	return false
}

type logSourceDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// Status is the node's own status; only set for kind "node".
	Status string `json:"status,omitempty"`
}

// handleLogSources implements GET /api/logs/sources (sudo only): the two panel
// processes plus every node.
func (h *Handler) handleLogSources(c *gin.Context) {
	nodes, err := h.store.Queries.ListNodes(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list nodes"})
		return
	}
	out := make([]logSourceDTO, 0, 2+len(nodes))
	out = append(out,
		logSourceDTO{ID: logSourcePanel, Label: "Panel API", Kind: "panel"},
		logSourceDTO{ID: logSourceBackend, Label: "Backend jobs", Kind: "backend"},
	)
	for _, n := range nodes {
		out = append(out, logSourceDTO{ID: logstream.NodeSource(n.ID), Label: n.Name, Kind: "node", Status: n.Status})
	}
	c.JSON(http.StatusOK, out)
}

type logsResponse struct {
	Entries   []logstream.Entry `json:"entries"`
	Next      int64             `json:"next"`
	Streaming bool              `json:"streaming"`
}

// handleGetLogs implements GET /api/logs (sudo only).
//
//	source  panel | backend | node:<id>          (required)
//	after   return only entries with id > after  (0: the newest `limit`, oldest first)
//	limit   default 300, at most 1000
//	level   minimum level: debug | info | warn | error
//	q       case-insensitive substring of the line
//
// Reading a node's logs also (re)sets its watch key, which is what makes the
// node start streaming them; `streaming` says whether it currently is. Redis
// down: the source this process itself publishes is served from its in-memory
// ring instead, everything else answers 503.
func (h *Handler) handleGetLogs(c *gin.Context) {
	ctx := c.Request.Context()

	source := c.Query("source")
	if source == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "source is required"})
		return
	}
	var after int64
	if v := c.Query("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "after must be a non-negative integer"})
			return
		}
		after = n
	}
	limit := defaultLogLimit
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "limit must be an integer"})
			return
		}
		if n > 0 {
			limit = min(n, maxLogLimit)
		}
	}
	minRank := 0
	if v := strings.TrimSpace(c.Query("level")); v != "" && !strings.EqualFold(v, "all") {
		rank, ok := logstream.LevelRank(v)
		if !ok {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "level must be one of debug, info, warn, error"})
			return
		}
		minRank = rank
	}
	needle := strings.ToLower(c.Query("q"))
	filtered := minRank > 0 || needle != ""

	// Resolve the source. Node ids must exist: a typo is a 404, not an empty log.
	var nodeID int32
	isNode := false
	switch {
	case source == logSourcePanel || source == logSourceBackend:
	case strings.HasPrefix(source, logSourceNodePfx):
		id, err := strconv.ParseInt(strings.TrimPrefix(source, logSourceNodePfx), 10, 32)
		if err != nil || id < 1 {
			c.JSON(http.StatusNotFound, gin.H{"detail": "Unknown log source"})
			return
		}
		if _, err := h.store.Queries.GetNodeByID(ctx, int32(id)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"detail": "Unknown log source"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not look up node"})
			return
		}
		nodeID, isNode = int32(id), true
	default:
		c.JSON(http.StatusNotFound, gin.H{"detail": "Unknown log source"})
		return
	}

	rdb := h.store.Cache.Raw()
	streaming := true // a panel process publishes continuously
	if isNode {
		pipe := rdb.Pipeline()
		pipe.Set(ctx, logstream.WatchKey(nodeID), "1", logWatchTTL)
		live := pipe.Exists(ctx, presenceNodeTotalKey(nodeID))
		if _, err := pipe.Exec(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"detail": "Log store (Redis) unavailable"})
			return
		}
		streaming = live.Val() > 0
	}

	entries, err := h.readLogEntries(c, source, after, limit, filtered)
	if err != nil {
		// The ring only holds this process' own output, so it can stand in for
		// exactly that source and no other.
		if source == logstream.ProcessSource() && !isNode {
			all := logstream.ProcessRing().Tail(0)
			if after > 0 {
				all = logstream.ProcessRing().After(after, 0)
			}
			entries = pickLogPage(all, after, limit, minRank, needle)
			c.JSON(http.StatusOK, logsResponse{Entries: entries, Next: nextLogID(entries, after), Streaming: true})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"detail": "Log store (Redis) unavailable"})
		return
	}
	entries = pickLogPage(entries, after, limit, minRank, needle)
	c.JSON(http.StatusOK, logsResponse{Entries: entries, Next: nextLogID(entries, after), Streaming: streaming})
}

// readLogEntries fetches, sorted by id, enough of a source's Redis list to
// answer the request: just the tail when that is enough, the whole list when a
// filter means matches may be anywhere in it.
//
// The list is normally already in id order (one publisher per source), but
// several panel replicas share the "panel" list and interleave their batches, so
// the result is sorted rather than trusted.
func (h *Handler) readLogEntries(c *gin.Context, source string, after int64, limit int, filtered bool) ([]logstream.Entry, error) {
	ctx := c.Request.Context()
	rdb := h.store.Cache.Raw()
	key := logstream.Key(source)

	n := limit
	if filtered {
		n = logstream.RingSize
	}
	for {
		raw, err := rdb.LRange(ctx, key, int64(-n), -1).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, err
		}
		entries := make([]logstream.Entry, 0, len(raw))
		for _, r := range raw {
			if e, ok := logstream.Decode(r); ok {
				entries = append(entries, e)
			}
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

		// Done when the whole list was read, or when the oldest entry read is
		// already at or before `after` (so nothing newer can lie further back).
		everything := len(raw) < n || n >= logstream.RingSize
		covered := after == 0 || (len(entries) > 0 && entries[0].ID <= after)
		if everything || covered {
			return entries, nil
		}
		n = min(n*2, logstream.RingSize)
	}
}

// pickLogPage applies the filters to id-ordered entries and cuts the page: the
// newest `limit` matches when there is no cursor, the oldest `limit` matches
// after the cursor otherwise (so a reader that falls behind never skips lines).
func pickLogPage(all []logstream.Entry, after int64, limit, minRank int, needle string) []logstream.Entry {
	out := make([]logstream.Entry, 0, min(len(all), limit))
	for _, e := range all {
		if e.ID > after && e.Matches(minRank, needle) {
			out = append(out, e)
		}
	}
	if len(out) > limit {
		if after == 0 {
			out = out[len(out)-limit:]
		} else {
			out = out[:limit]
		}
	}
	return out
}

// nextLogID is the cursor to send back: the newest id returned, or the one
// the caller passed when nothing new matched.
func nextLogID(entries []logstream.Entry, after int64) int64 {
	if len(entries) == 0 {
		return after
	}
	return entries[len(entries)-1].ID
}
