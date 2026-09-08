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

// StatusHost is one entry of StatusResult.Hosts - mirrors
// internal/httpapi/gateway_status.go's gatewayHostDTO field-for-field
// (same reasoning as UserSyncPayload on why this isn't literally the same
// Go type: this package can't import internal/httpapi).
type StatusHost struct {
	ID              int32   `json:"id"`
	Remark          string  `json:"remark"`
	Address         string  `json:"address"`
	Port            *int32  `json:"port"`
	Path            *string `json:"path"`
	SNI             *string `json:"sni"`
	Host            *string `json:"host"`
	Security        string  `json:"security"`
	ALPN            string  `json:"alpn"`
	Fingerprint     string  `json:"fingerprint"`
	AllowInsecure   *bool   `json:"allowinsecure"`
	IsDisabled      *bool   `json:"is_disabled"`
	MuxEnable       bool    `json:"mux_enable"`
	FragmentSetting *string `json:"fragment_setting"`
	NoiseSetting    *string `json:"noise_setting"`
	RandomUserAgent bool    `json:"random_user_agent"`
	UseSNIAsHost    bool    `json:"use_sni_as_host"`
	Priority        int32   `json:"priority"`
	InboundTag      string  `json:"inbound_tag"`
	Protocol        string  `json:"protocol"`
}

// StatusResult mirrors gatewayStatusDTO - a peer's live crowdedness score
// plus its own real, enabled hosts. Consumed today by the admin-facing
// "Test connection"-adjacent calls this package's callers make directly;
// sub-phase 4's background refresh job (not built yet) will be the real
// periodic caller.
type StatusResult struct {
	Crowdedness int          `json:"crowdedness"`
	Hosts       []StatusHost `json:"hosts"`
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
