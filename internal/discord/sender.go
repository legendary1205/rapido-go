// Package discord posts notification embeds to Discord incoming webhooks.
// The current Python system has no real Discord bot (no gateway connection,
// no slash commands) - just plain outgoing webhook POSTs - so this package
// mirrors that shape exactly.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

type EmbedPayload struct {
	Content string  `json:"content"`
	Embeds  []Embed `json:"embeds"`
}

type Embed struct {
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description"`
	Color       int     `json:"color"`
	Footer      *Footer `json:"footer,omitempty"`
}

type Footer struct {
	Text string `json:"text"`
}

type Sender struct {
	httpClient *http.Client
}

func NewSender(httpClient *http.Client) *Sender {
	return &Sender{httpClient: httpClient}
}

// Send mirrors send_webhooks: POSTs payload to globalWebhook (if non-empty)
// and to adminWebhook (if non-nil and non-empty) - independently, both.
//
// Fixed vs. Python: requests.post(webhook, json=...) there has no timeout
// at all, so a webhook host that accepts the TCP connection and never
// responds hangs the calling request/job indefinitely. Sender's httpClient
// (see internal/report's construction in cmd/panel/main.go) is expected to
// carry a Timeout, bounding every call here.
func (s *Sender) Send(ctx context.Context, payload EmbedPayload, globalWebhook string, adminWebhook *string, logger *slog.Logger) {
	if globalWebhook != "" {
		s.post(ctx, globalWebhook, payload, logger)
	}
	if adminWebhook != nil && *adminWebhook != "" {
		s.post(ctx, *adminWebhook, payload, logger)
	}
}

func (s *Sender) post(ctx context.Context, webhook string, payload EmbedPayload, logger *slog.Logger) {
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Warn("discord: could not encode webhook payload", "error", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		logger.Warn("discord: could not build webhook request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		logger.Warn("discord: webhook POST failed", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		logger.Warn("discord: webhook POST returned non-2xx", "status", resp.StatusCode)
	}
}
