package loadmap

import (
	"context"
	"errors"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// Redis keys written by the node-live endpoint (see internal/httpapi/nodelive.go).
const (
	livePresenceKey = "presence:live"
)

func nodePortsKey(id int32) string { return "presence:node:" + strconv.Itoa(int(id)) + ":ports" }
func nodeTotalKey(id int32) string { return "presence:node:" + strconv.Itoa(int(id)) + ":total" }

type redisPresence struct {
	rdb redis.Cmdable
}

// NewRedisPresence reads presence from Redis: presence:live, and for every
// node presence:node:<id>:ports (HASH port -> open connections) and
// presence:node:<id>:total (STRING, the node's open client connections; its
// existence is what says the node is reporting, even when no port has a
// connection and the hash is therefore absent).
func NewRedisPresence(rdb redis.Cmdable) Presence { return redisPresence{rdb: rdb} }

// Read fetches everything in one pipelined round trip.
func (r redisPresence) Read(ctx context.Context, nodeIDs []int32) (PresenceData, error) {
	pipe := r.rdb.Pipeline()
	live := pipe.Exists(ctx, livePresenceKey)
	totals := make([]*redis.StringCmd, len(nodeIDs))
	ports := make([]*redis.MapStringStringCmd, len(nodeIDs))
	for i, id := range nodeIDs {
		totals[i] = pipe.Get(ctx, nodeTotalKey(id))
		ports[i] = pipe.HGetAll(ctx, nodePortsKey(id))
	}
	// A node whose total key is gone makes its GET return redis.Nil, which
	// Exec reports as its error; that is an answer, not a failure.
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return PresenceData{}, err
	}

	data := PresenceData{Nodes: make(map[int32]NodePresence, len(nodeIDs))}
	n, err := live.Result()
	if err != nil {
		return PresenceData{}, err
	}
	data.Live = n > 0
	if !data.Live {
		return data, nil
	}
	for i, id := range nodeIDs {
		totalRaw, terr := totals[i].Result()
		hasTotal := terr == nil
		if terr != nil && !errors.Is(terr, redis.Nil) {
			continue
		}
		raw, perr := ports[i].Result()
		if perr != nil {
			continue
		}
		np := NodePresence{Reporting: hasTotal || len(raw) > 0, Ports: make(map[int]int, len(raw))}
		for field, value := range raw {
			port, e1 := strconv.Atoi(field)
			conns, e2 := strconv.Atoi(value)
			if e1 != nil || e2 != nil || port <= 0 || conns < 0 {
				continue
			}
			np.Ports[port] = conns
		}
		if hasTotal {
			if total, e := strconv.Atoi(totalRaw); e == nil && total > 0 {
				np.Total = total
			}
		}
		data.Nodes[id] = np
	}
	return data, nil
}
