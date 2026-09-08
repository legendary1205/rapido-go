// Package gatewayclient is the outbound side of panel-to-panel calls for
// the multi-panel load balancer (Gateway) - a thin HTTP client any panel
// uses to reach a peer's own /api/internal/gateway/* endpoints, which are
// exactly the ones internal/httpapi/gateway.go serves on the receiving
// side. Kept separate from internal/httpapi (which is otherwise all
// inbound HTTP) so the background refresh job planned for a later
// sub-phase can reuse it without importing the whole handler package.
package gatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// httpClient has an explicit timeout - an unreachable or hung peer must
// never block the caller indefinitely, whether that caller is an admin's
// "test connection" click or (in a later sub-phase) a periodic background
// refresh with many peers to get through.
var httpClient = &http.Client{Timeout: 5 * time.Second}

// PingResult is what a healthy peer's GET /internal/gateway/ping returns -
// deliberately minimal (see internal/httpapi/gateway.go's own doc comment
// on that handler): enough to prove the secret was accepted and identify
// which panel answered, nothing a peer wouldn't already know about itself.
type PingResult struct {
	PanelName string `json:"panel_name"`
}

// Ping calls a peer's own ping endpoint using the secret THAT peer expects
// (see the Gateway migration's own doc comment on why this is
// asymmetric/per-installation rather than a shared pair secret).
func Ping(ctx context.Context, baseURL, secret string) (PingResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/internal/gateway/ping", nil)
	if err != nil {
		return PingResult{}, fmt.Errorf("could not build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)

	resp, err := httpClient.Do(req)
	if err != nil {
		return PingResult{}, fmt.Errorf("could not reach peer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PingResult{}, fmt.Errorf("peer responded with status %d", resp.StatusCode)
	}
	var result PingResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return PingResult{}, fmt.Errorf("could not decode peer response: %w", err)
	}
	return result, nil
}

// UserSyncPayload mirrors internal/httpapi/gateway_sync.go's
// gatewaySyncPayload - a full snapshot of one user's identity/policy/
// credentials (not a diff), sent by the panel that actually owns the
// user to every peer it's configured with. Deliberately doesn't wrap
// this in the same struct on both ends: internal/httpapi owns the real
// definition (it also has to read this shape back out of an inbound
// request), this is just the outbound wire copy so gatewayclient doesn't
// import internal/httpapi (which would be the wrong dependency direction
// - httpapi is the one importing this package, not the other way round).
type UserSyncPayload struct {
	OriginPanelName        string                     `json:"origin_panel_name"`
	Username               string                     `json:"username"`
	Deleted                bool                       `json:"deleted"`
	Status                 string                     `json:"status,omitempty"`
	DataLimit              *int64                     `json:"data_limit,omitempty"`
	DataLimitResetStrategy string                     `json:"data_limit_reset_strategy,omitempty"`
	Expire                 *int64                     `json:"expire,omitempty"`
	Proxies                map[string]json.RawMessage `json:"proxies,omitempty"`
}

// SyncUserResult is deliberately tiny - the caller (a fire-and-forget
// background goroutine, see internal/httpapi/gateway_sync.go's dispatch
// helper) only ever logs success/failure, never acts on the response
// body beyond that.
type SyncUserResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// SyncUser pushes one user's full state to a peer - create/update if
// payload.Deleted is false, delete if true. Same secret/timeout/error
// shape as Ping.
func SyncUser(ctx context.Context, baseURL, secret string, payload UserSyncPayload) (SyncUserResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return SyncUserResult{}, fmt.Errorf("could not encode payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/internal/gateway/users/sync", bytes.NewReader(body))
	if err != nil {
		return SyncUserResult{}, fmt.Errorf("could not build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return SyncUserResult{}, fmt.Errorf("could not reach peer: %w", err)
	}
	defer resp.Body.Close()

	var result SyncUserResult
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != http.StatusOK {
		if result.Detail == "" {
			result.Detail = fmt.Sprintf("peer responded with status %d", resp.StatusCode)
		}
		return result, fmt.Errorf("peer rejected the sync: %s", result.Detail)
	}
	return result, nil
}

// EffectiveHost mirrors subscription.EffectiveInbound field-for-field -
// gatewayclient can't import internal/subscription's own type by value
// across the wire boundary without also importing internal/db/generated
// transitively (subscription.BuildEffectiveInbound's parameters pull that
// in) for zero benefit, since only the JSON shape is needed here, not the
// builder function. Already a public-safe, fully-merged view (real Reality
// PUBLIC key, never a private key) - see gateway_status.go's own doc
// comment on why sending exactly this, not raw host+inbound rows, needed
// no separate "what's safe to expose" pass.
type EffectiveHost struct {
	Tag              string `json:"tag"`
	Protocol         string `json:"protocol"`
	Network          string `json:"network"`
	HeaderType       string `json:"header_type"`
	Port             int    `json:"port"`
	Address          string `json:"address"`
	SNI              string `json:"sni"`
	HostHeader       string `json:"host_header"`
	Path             string `json:"path"`
	Security         string `json:"security"`
	ALPN             string `json:"alpn"`
	Fingerprint      string `json:"fingerprint"`
	AllowInsecure    bool   `json:"allow_insecure"`
	RealityPublicKey string `json:"reality_public_key"`
	RealityShortID   string `json:"reality_short_id"`
	MuxEnable        bool   `json:"mux_enable"`
	FragmentSetting  string `json:"fragment_setting"`
	NoiseSetting     string `json:"noise_setting"`
	RandomUserAgent  bool   `json:"random_user_agent"`

	// Remark is the raw, unformatted template ({USERNAME} etc. still
	// literal) - the RECEIVING panel formats it with its own user's own
	// variables, never this peer's, since {DATA_USAGE}/{EXPIRE_DATE}/etc.
	// only make sense relative to the actual subscriber.
	Remark   string `json:"remark"`
	Priority int32  `json:"priority"`
}

// StatusResult mirrors gatewayStatusDTO - a peer's live crowdedness score
// plus its own real, enabled hosts. Fetched periodically by
// internal/gatewayjob's background refresh loop and cached in Redis - see
// that package and internal/httpapi/subscription.go's forEachUserHost,
// the reader.
type StatusResult struct {
	Crowdedness int             `json:"crowdedness"`
	Hosts       []EffectiveHost `json:"hosts"`
}

// Status calls a peer's GET /internal/gateway/status. Same secret/timeout
// shape as Ping.
func Status(ctx context.Context, baseURL, secret string) (StatusResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/internal/gateway/status", nil)
	if err != nil {
		return StatusResult{}, fmt.Errorf("could not build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)

	resp, err := httpClient.Do(req)
	if err != nil {
		return StatusResult{}, fmt.Errorf("could not reach peer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return StatusResult{}, fmt.Errorf("peer responded with status %d", resp.StatusCode)
	}
	var result StatusResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return StatusResult{}, fmt.Errorf("could not decode peer response: %w", err)
	}
	return result, nil
}
