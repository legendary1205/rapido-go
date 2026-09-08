package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/gatewayclient"
)

// seedPeerStatus writes a fake internal/gatewayjob-shaped cache entry
// directly, bypassing any real network call - exactly the same read path
// forEachUserHost's gatherPeerHosts uses in production, just with a
// deterministic fixture instead of a real peer's real response.
func seedPeerStatus(t *testing.T, handler *Handler, peerID int32, status gatewayclient.StatusResult) {
	t.Helper()
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if err := handler.store.Cache.Set(context.Background(), cache.GatewayPeerStatusKey(peerID), string(raw), 10*time.Minute); err != nil {
		t.Fatalf("seed peer status cache: %v", err)
	}
}

func createTestGatewayPeer(t *testing.T, router http.Handler, token, name string, enabled bool) int32 {
	t.Helper()
	resp := doRequest(t, router, "POST", "/api/settings/gateway/peers", token, map[string]interface{}{
		"name": name, "base_url": "http://127.0.0.1:9", "secret": "unused-in-this-test", "enabled": enabled,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("create peer %s: %d %v", name, resp.Code, resp.Body)
	}
	id, _ := resp.Body["id"].(float64)
	return int32(id)
}

// decodeV2raySubscription fetches the raw v2ray-format subscription
// (explicit format route, bypasses User-Agent sniffing) and returns its
// decoded newline-separated links.
func decodeV2raySubscription(t *testing.T, router http.Handler, subURL string) []string {
	t.Helper()
	resp := doRequest(t, router, "GET", subURL+"/v2ray", "", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("fetch subscription: %d", resp.Code)
	}
	raw, err := base64.StdEncoding.DecodeString(string(resp.Raw))
	if err != nil {
		t.Fatalf("decode subscription body: %v", err)
	}
	var links []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" {
			links = append(links, l)
		}
	}
	return links
}

// TestSubscriptionMergesPeerHostsAfterLocalHosts is the core proof for
// Gateway sub-phase 4: a user's subscription includes not just their own
// panel's hosts but every enabled peer's cached hosts too - filtered to
// protocols the user actually has locally, appended strictly after every
// local host, and never including a disabled peer or a protocol the user
// doesn't have.
func TestSubscriptionMergesPeerHostsAfterLocalHosts(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "Merge VLESS", "protocol": "vless"}})

	created := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "merge_carol",
		"proxies": map[string]interface{}{
			"vless": map[string]string{"id": "33333333-3333-3333-3333-333333333333"},
		},
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create user: %d %v", created.Code, created.Body)
	}
	subURL, _ := created.Body["subscription_url"].(string)
	if subURL == "" {
		t.Fatalf("no subscription_url in create response: %v", created.Body)
	}

	enabledPeerID := createTestGatewayPeer(t, router, token, "Enabled Peer", true)
	disabledPeerID := createTestGatewayPeer(t, router, token, "Disabled Peer", false)

	// The enabled peer offers a vless host (matches merge_carol's own
	// protocol - must appear) and a trojan host (merge_carol has no trojan
	// proxy - must NOT appear).
	seedPeerStatus(t, handler, enabledPeerID, gatewayclient.StatusResult{
		Crowdedness: 2,
		Hosts: []gatewayclient.EffectiveHost{
			{Tag: "Peer VLESS", Protocol: "vless", Network: "tcp", Port: 443, Address: "peer.example.test",
				Security: "none", Remark: "Peer Host ({USERNAME})", Priority: 0},
			{Tag: "Peer TROJAN", Protocol: "trojan", Network: "tcp", Port: 444, Address: "peer.example.test",
				Security: "none", Remark: "Peer Trojan ({USERNAME})", Priority: 0},
		},
	})
	// The disabled peer's cache entry exists but must be ignored entirely -
	// a peer being disabled must hide its hosts even if a stale cache entry
	// is still sitting in Redis from before it was disabled.
	seedPeerStatus(t, handler, disabledPeerID, gatewayclient.StatusResult{
		Crowdedness: 0,
		Hosts: []gatewayclient.EffectiveHost{
			{Tag: "Disabled Peer VLESS", Protocol: "vless", Network: "tcp", Port: 443, Address: "disabled.example.test",
				Security: "none", Remark: "Disabled Peer Host", Priority: 0},
		},
	})

	links := decodeV2raySubscription(t, router, subURL)
	if len(links) != 2 {
		t.Fatalf("expected exactly 2 links (1 local + 1 enabled-peer vless), got %d: %v", len(links), links)
	}
	if !strings.Contains(links[0], "Merge+VLESS") && !strings.Contains(links[0], "merge_carol") {
		t.Errorf("expected the LOCAL host link first, got %v", links[0])
	}
	if !strings.Contains(links[1], "Enabled+Peer") && !strings.Contains(links[1], "Enabled%20Peer") {
		t.Errorf("expected the enabled peer's link second (after the local host), got %v", links[1])
	}
	for _, l := range links {
		if strings.Contains(l, "Disabled") {
			t.Errorf("disabled peer's host leaked into the subscription: %v", l)
		}
		if strings.Contains(l, "trojan") || strings.Contains(strings.ToLower(l), "peer+trojan") {
			t.Errorf("a protocol the user doesn't have locally leaked into the subscription: %v", l)
		}
	}
}

// TestSubscriptionIgnoresPeerWithNoCachedStatus proves a peer that
// gatewayjob has never successfully reached (no cache entry at all, not
// even a stale one) is silently omitted - never an error surfaced to the
// client waiting on their subscription.
func TestSubscriptionIgnoresPeerWithNoCachedStatus(t *testing.T) {
	router, token, _ := newTestRouterAndHandler(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "Unreached VLESS", "protocol": "vless"}})

	created := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "merge_dave",
		"proxies": map[string]interface{}{
			"vless": map[string]string{"id": "44444444-4444-4444-4444-444444444444"},
		},
	})
	subURL, _ := created.Body["subscription_url"].(string)

	createTestGatewayPeer(t, router, token, "Unreachable Peer", true) // never seeded in cache

	links := decodeV2raySubscription(t, router, subURL)
	if len(links) != 1 {
		t.Fatalf("expected only the 1 local link (peer has no cached status yet), got %d: %v", len(links), links)
	}
}
