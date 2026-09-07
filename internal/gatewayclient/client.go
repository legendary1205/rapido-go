// Package gatewayclient is the outbound side of panel-to-panel calls for
// the multi-panel load balancer (Gateway) - a thin HTTP client any panel
// uses to reach a peer's own /api/internal/gateway/* endpoints, which are
// exactly the ones internal/httpapi/gateway.go serves on the receiving
// side. Kept separate from internal/httpapi (which is otherwise all
// inbound HTTP) so the background refresh job planned for a later
// sub-phase can reuse it without importing the whole handler package.
package gatewayclient

import (
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
