// Package telegram is the one-way half of the Telegram integration: it sends
// admin notifications via the Bot API's sendMessage endpoint, and holds no
// connection or state between sends. The interactive half - the long-polling
// admin console with inline keyboards - is internal/telegrambot; the two share
// the bot token and admin list from the integration settings but nothing else.
// See internal/report for the event-level dispatch policy this package's
// Report is called from.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
)

const defaultBaseURL = "https://api.telegram.org"

// Config is the subset of a resolved integrationsettings.Values this
// package needs - kept as its own narrow struct (not
// integrationsettings.Values itself) so this package has zero dependency on
// integrationsettings or the DB layer, matching
// internal/subscription/vars.go's UserInfo convention.
type Config struct {
	APIToken        string
	AdminIDs        []int64
	LoggerChannelID int64  // 0 = unset
	LoggerTopicID   int64  // 0 = unset
	ProxyURL        string // SOCKS/HTTP proxy for the Bot API call; "" = direct
}

// Enabled mirrors the current Python bot's `if bot and (...)` guard: no
// token means no bot object, so nothing is ever attempted.
func (c Config) Enabled() bool {
	return c.APIToken != "" && (len(c.AdminIDs) > 0 || c.LoggerChannelID != 0)
}

type Sender struct {
	httpClient *http.Client
	baseURL    string
}

// NewSender builds a Sender. baseURL overrides the real Bot API host - pass
// "" in production (defaults to https://api.telegram.org) and a local
// httptest.Server URL in tests.
func NewSender(httpClient *http.Client, baseURL string) *Sender {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Sender{httpClient: httpClient, baseURL: baseURL}
}

// Report mirrors app/telegram/handlers/report.py's report(): if a logger
// channel is configured, send only there (with its topic thread id if also
// set); otherwise fan out to every admin ID. Separately, DM dmChatID
// directly when non-nil - the per-owning-admin notification channel driven
// by admins.telegram_id.
//
// Fixed vs. Python: report() there wraps the whole channel-or-admin-loop
// (and the DM) in one try/except, so one bad chat ID silently skips every
// remaining recipient, including the owning admin's DM. Here every
// recipient is sent independently; one failing send is logged and does not
// affect any other.
func (s *Sender) Report(ctx context.Context, cfg Config, text string, dmChatID *int64, logger *slog.Logger) {
	if !cfg.Enabled() {
		return
	}

	if cfg.LoggerChannelID != 0 {
		s.send(ctx, cfg, cfg.LoggerChannelID, text, cfg.LoggerTopicID, logger)
	} else {
		for _, adminID := range cfg.AdminIDs {
			s.send(ctx, cfg, adminID, text, 0, logger)
		}
	}

	if dmChatID != nil {
		s.send(ctx, cfg, *dmChatID, text, 0, logger)
	}
}

type sendMessageRequest struct {
	ChatID          int64  `json:"chat_id"`
	Text            string `json:"text"`
	ParseMode       string `json:"parse_mode"`
	MessageThreadID int64  `json:"message_thread_id,omitempty"`
}

func (s *Sender) send(ctx context.Context, cfg Config, chatID int64, text string, threadID int64, logger *slog.Logger) {
	body, err := json.Marshal(sendMessageRequest{
		ChatID: chatID, Text: text, ParseMode: "HTML", MessageThreadID: threadID,
	})
	if err != nil {
		logger.Warn("telegram: could not encode sendMessage body", "error", err)
		return
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", s.baseURL, cfg.APIToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		logger.Warn("telegram: could not build sendMessage request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := s.httpClient
	if cfg.ProxyURL != "" {
		// A one-off client wrapping a proxying transport for this call only -
		// this is not a hot path (one notification event, not one per
		// request), so rebuilding a client here rather than caching a
		// per-proxy-URL client is a deliberate simplicity/perf trade-off.
		if proxyURL, err := url.Parse(cfg.ProxyURL); err == nil {
			client = &http.Client{
				Timeout:   s.httpClient.Timeout,
				Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
			}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		logger.Warn("telegram: sendMessage failed", "chat_id", chatID, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		logger.Warn("telegram: sendMessage returned non-2xx", "chat_id", chatID, "status", resp.StatusCode)
	}
}
