// Package resellerapi is an HTTP client for an external reseller/wallet-management
// bot (panel -> bot, one-way; see app/resellerapi/manager.py for the Python
// implementation this ports). It covers all three of that bot's duties:
// GetConfigs (inbound filtering per reseller), GetUsersLimit (per-admin
// active-user cap) and ReportUsages (the per-admin traffic the bot bills
// resellers for - queued by node reports and sent by internal/resellerusagejob).
package resellerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Config is the subset of a resolved integrationsettings.Values this
// package needs - kept narrow like internal/telegram.Config, with zero
// dependency on integrationsettings itself.
type Config struct {
	Secret string
	URL    string
}

// Enabled mirrors ResellerAPI.is_enabled(): an unset secret means no bot, so
// every method below returns immediately with no outbound request at all.
func (c Config) Enabled() bool {
	return c.Secret != ""
}

// CanReportUsage is Enabled plus a URL to send to. The two lookups below
// tolerate a missing URL by failing quietly, but usage is only queued for
// billing when a report can actually be delivered.
func (c Config) CanReportUsage() bool {
	return c.Enabled() && c.URL != ""
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

// GetConfigs mirrors ResellerAPI.get_configs: POSTs configs (protocol -> tags) to
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

// GetUsersLimit mirrors ResellerAPI.get_users_limit: GETs
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

// Usage is one admin's traffic for a usage report, in bytes.
type Usage struct {
	Username string `json:"username"`
	Usage    int64  `json:"usage"`
}

// ReportUsages mirrors ResellerAPI.report_admin_usage's POST: sends
// [{"username", "usage"}] to {url}/api/subscriptions/{secret}/usages, where
// the bot charges each reseller's wallet for that traffic. Unlike the two
// lookups above, failure is returned rather than swallowed - the caller
// keeps the usage queued and sends it again, since a report that silently
// vanished is traffic nobody paid for.
func (c *Client) ReportUsages(ctx context.Context, cfg Config, usages []Usage) error {
	if !cfg.CanReportUsage() {
		return errors.New("reseller API is not configured")
	}
	body, err := json.Marshal(usages)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.baseURL()+"/usages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("usage report rejected with HTTP %d", resp.StatusCode)
	}
	return nil
}
