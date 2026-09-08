// Package gatewayjob is the periodic BACKEND-role-only loop that keeps
// each enabled peer's live crowdedness/host status fresh in Redis, so
// internal/httpapi/subscription.go's forEachUserHost never makes a
// synchronous network call to a peer while a real client is waiting on
// their subscription - the same "never per-refresh round-trip cost"
// philosophy internal/hostmetrics/monitoring already follows for this
// panel's own nodes, applied one hop further out. Mirrors
// internal/reviewjob's own Run(ctx, ..., interval) shape.
package gatewayjob

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/gatewayclient"
)

// peerCallTimeout bounds one peer's status call - an unreachable or hung
// peer must never hold up refreshing every OTHER peer's entry, let alone
// the next tick.
const peerCallTimeout = 10 * time.Second

// peerStatusTTL is deliberately longer than the intended refresh interval
// (a few minutes, set by the caller) - if one or two consecutive refreshes
// fail for a peer (a transient network blip), its last-known-good status
// keeps being used rather than immediately vanishing from subscription
// merging. Once a peer has been unreachable for long enough that even this
// TTL lapses, its entry disappears from Redis entirely and
// forEachUserHost's cache read (a plain miss, no fallback - see its own
// doc comment) naturally stops including it, rather than serving
// increasingly stale host data forever.
const peerStatusTTL = 10 * time.Minute

// Run ticks every interval until ctx is canceled, refreshing every enabled
// peer's cached status once per tick. Like reviewjob.Run, one bad tick
// (or one bad peer within a tick) never stops the loop.
func Run(ctx context.Context, q *generated.Queries, cacheClient *cache.Client, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh(ctx, q, cacheClient, logger)
		}
	}
}

// refresh fans out one goroutine per enabled peer so a single slow/hung
// peer can't delay refreshing every other one - each call is independently
// bounded by peerCallTimeout regardless of how long the tick itself takes.
func refresh(ctx context.Context, q *generated.Queries, cacheClient *cache.Client, logger *slog.Logger) {
	peers, err := q.ListGatewayPeers(ctx)
	if err != nil {
		logger.Warn("gatewayjob: could not list peers", "error", err)
		return
	}
	for _, peer := range peers {
		if !peer.Enabled {
			continue
		}
		peer := peer
		go refreshOne(ctx, cacheClient, logger, peer)
	}
}

func refreshOne(ctx context.Context, cacheClient *cache.Client, logger *slog.Logger, peer generated.GatewayPeer) {
	callCtx, cancel := context.WithTimeout(ctx, peerCallTimeout)
	defer cancel()

	status, err := gatewayclient.Status(callCtx, peer.BaseUrl, peer.Secret)
	if err != nil {
		// Not an error worth escalating on its own - a peer being briefly
		// unreachable is an expected, routine occurrence (network blip,
		// the peer restarting for its own deploy), and its previous
		// cached status keeps being used until peerStatusTTL lapses. Still
		// logged, never silently swallowed - a peer that stays unreachable
		// for a long time should be visible in the logs, not just quietly
		// missing from subscriptions with no trace of why.
		logger.Warn("gatewayjob: peer status refresh failed", "peer", peer.Name, "error", err)
		return
	}

	raw, err := json.Marshal(status)
	if err != nil {
		logger.Warn("gatewayjob: could not encode peer status", "peer", peer.Name, "error", err)
		return
	}
	if err := cacheClient.Set(ctx, cache.GatewayPeerStatusKey(peer.ID), string(raw), peerStatusTTL); err != nil {
		logger.Warn("gatewayjob: could not cache peer status", "peer", peer.Name, "error", err)
	}
}
