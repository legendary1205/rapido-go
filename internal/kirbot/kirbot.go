// Package kirbot is an HTTP client for an external reseller/wallet-management
// bot (panel -> bot, one-way; see app/kirbot/manager.py for the current
// Python implementation this ports). Two of its three Python duties are
// ported here: GetConfigs (inbound filtering per reseller) and
// GetUsersLimit (per-admin active-user cap).
//
// report_admin_usage (billing/usage reporting) is deliberately NOT ported:
// it needs a per-user usage-delta data source this rewrite doesn't have
// yet - Phase 3's Go node agent has no stats-reporting endpoint, and no
// record_user_usages-equivalent job exists. This is a real, flagged gap,
// not a stub returning fake numbers.
package kirbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Config is the subset of a resolved integrationsettings.Values this
// package needs - kept narrow like internal/telegram.Config, with zero
// dependency on integrationsettings itself.
type Config struct {
	Secret string
	URL    string
}

// Enabled mirrors Kirbot.is_enabled(): an unset secret means no bot, so
// every method below returns immediately with no outbound request at all.
func (c Config) Enabled() bool {
	return c.Secret != ""
}

func (c Config) baseURL() string {
	return fmt.Sprintf("%s/api/subscriptions/%s", c.URL, c.Secret)
}

type Client struct {
	httpClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	return &Client{httpClient: httpClient}
}

// GetConfigs mirrors Kirbot.get_configs: POSTs configs (protocol -> tags) to
// {url}/api/subscriptions/{secret}/{username}/configs and returns the
// filtered result. Returns nil on a disabled bot, any network/HTTP/decode
// error, or a non-2xx response - advisory only, never blocks or fails the
// caller (the caller falls back to the unfiltered configs it passed in).
func (c *Client) GetConfigs(ctx context.Context, cfg Config, username string, configs map[string][]string) map[string][]string {
	if !cfg.Enabled() {
		return nil
	}
	body, err := json.Marshal(configs)
	if err != nil {
		return nil
	}
	endpoint := fmt.Sprintf("%s/%s/configs", cfg.baseURL(), username)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	var filtered map[string][]string
	if err := json.NewDecoder(resp.Body).Decode(&filtered); err != nil {
		return nil
	}
	return filtered
}

// GetUsersLimit mirrors Kirbot.get_users_limit: GETs
// {url}/api/subscriptions/{secret}/{username}/users_limit, expecting
// {"users_limit": N}. Returns nil (uncapped/unknown) on a disabled bot, any
// network/HTTP/decode error, or when the field is absent/null.
func (c *Client) GetUsersLimit(ctx context.Context, cfg Config, username string) *int {
	if !cfg.Enabled() {
		return nil
	}
	endpoint := fmt.Sprintf("%s/%s/users_limit", cfg.baseURL(), username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	var decoded struct {
		UsersLimit *int `json:"users_limit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil
	}
	return decoded.UsersLimit
}
