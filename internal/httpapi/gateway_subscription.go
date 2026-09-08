package httpapi

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/gatewayclient"
	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

// gatewayPeerHost is one peer's cached host, tagged with which peer it
// came from (for the remark prefix and the sort below) and that peer's
// live crowdedness (also for the sort) - see gatherPeerHosts.
type gatewayPeerHost struct {
	peerName    string
	crowdedness int
	host        gatewayclient.EffectiveHost
}

// gatherPeerHosts is Gateway sub-phase 4's actual subscription-merge step,
// called once per subscription request from forEachUserHost. Reads every
// enabled peer's LAST CACHED status (internal/gatewayjob's periodic
// background write, never a live call here - a real client waiting on
// their subscription must never pay for a peer's network round trip, the
// same reasoning host_metrics/crowdedness already follows one hop closer
// to home) and keeps only hosts whose protocol this user actually has a
// local proxy for (userProtocols is forEachUserHost's own
// settingsByProtocol map, keyed by proxy type) - that's the only case
// where the same uuid/password this user already has locally (see
// gateway_sync.go's own cross-panel sync) will actually authenticate
// against that peer's node at all.
//
// Sorted by (peer crowdedness ascending, then that peer's own host
// priority, then a stable name/tag tiebreak) - peer hosts are ranked
// among THEMSELVES this way, a less-crowded peer's hosts surfacing before
// a more-crowded one's. They are never interleaved with or reordered
// against the user's LOCAL hosts (forEachUserHost always emits every
// local host, in the admin's own configured order, before calling this
// at all) - an admin's manually-set local priority is never silently
// touched by which peer happens to be less busy right now.
func (h *Handler) gatherPeerHosts(ctx context.Context, userProtocols map[string]proxysettings.Settings) []gatewayPeerHost {
	if len(userProtocols) == 0 {
		return nil
	}
	peers, err := h.store.Queries.ListGatewayPeers(ctx)
	if err != nil {
		h.logger.Warn("gateway subscription merge: could not list peers", "error", err)
		return nil
	}

	var out []gatewayPeerHost
	for _, peer := range peers {
		if !peer.Enabled {
			continue
		}
		// A cache miss (peer never successfully refreshed yet, or its
		// entry aged past gatewayjob's own TTL) is routine, not an error -
		// that peer is simply omitted from this response, exactly as if
		// it weren't configured at all.
		raw, err := h.store.Cache.Get(ctx, cache.GatewayPeerStatusKey(peer.ID))
		if err != nil {
			continue
		}
		var status gatewayclient.StatusResult
		if err := json.Unmarshal([]byte(raw), &status); err != nil {
			h.logger.Warn("gateway subscription merge: could not decode cached peer status", "peer", peer.Name, "error", err)
			continue
		}
		for _, host := range status.Hosts {
			if _, ok := userProtocols[host.Protocol]; !ok {
				continue
			}
			out = append(out, gatewayPeerHost{peerName: peer.Name, crowdedness: status.Crowdedness, host: host})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].crowdedness != out[j].crowdedness {
			return out[i].crowdedness < out[j].crowdedness
		}
		if out[i].host.Priority != out[j].host.Priority {
			return out[i].host.Priority < out[j].host.Priority
		}
		if out[i].peerName != out[j].peerName {
			return out[i].peerName < out[j].peerName
		}
		return out[i].host.Tag < out[j].host.Tag
	})
	return out
}
