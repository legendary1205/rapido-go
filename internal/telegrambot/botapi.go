// Package telegrambot is the interactive half of the Telegram integration: a
// long-polling admin console driven by inline keyboards. The notification half
// (one-way messages when something happens) lives in internal/telegram.
//
// The console owns no business rules. Every action is an HTTP call into the
// panel's own router, made in-process with a short-lived token for the admin
// the Telegram account maps to, so validation, ownership scoping,
// notifications and cache invalidation are exactly what the dashboard gets.
package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.telegram.org"

// callTimeout bounds every non-polling Bot API call. Long-poll calls get their
// own deadline (poll timeout + slack) in getUpdates.
const callTimeout = 20 * time.Second

type tgUser struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	LanguageCode string `json:"language_code"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgMessage struct {
	MessageID     int             `json:"message_id"`
	From          *tgUser         `json:"from"`
	Chat          tgChat          `json:"chat"`
	Text          string          `json:"text"`
	ForwardDate   int64           `json:"forward_date,omitempty"`
	ForwardOrigin json.RawMessage `json:"forward_origin,omitempty"`
	ViaBot        *tgUser         `json:"via_bot,omitempty"`
}

// forwarded reports whether the message was forwarded from somewhere else, in
// either of the shapes the Bot API has used for it.
func (m *tgMessage) forwarded() bool {
	return m.ForwardDate != 0 || (len(m.ForwardOrigin) > 0 && string(m.ForwardOrigin) != "null")
}

type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type keyboard [][]button

// apiError is a Bot API "ok": false answer. Its text never contains the token.
type apiError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram api error %d: %s", e.Code, e.Description)
}

func isNotModified(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified")
}

type botClient struct {
	token   string
	baseURL string
	http    *http.Client
}

// newBotClient builds a client for one (token, proxy) pair. An unparsable proxy
// URL falls back to a direct connection rather than disabling the bot: the
// operator would otherwise see a silent console with no error to act on.
func newBotClient(token, baseURL, proxyURL string) *botClient {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	transport := &http.Transport{
		MaxIdleConns:        4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil && u.Host != "" {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &botClient{token: token, baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Transport: transport}}
}

// redact removes anything that could carry the bot token from a transport
// error: net/http embeds the full request URL, which contains it.
func (b *botClient) redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := strings.ReplaceAll(err.Error(), b.token, "<token>")
	return errors.New(msg)
}

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (b *botClient) endpoint(method string) string {
	return b.baseURL + "/bot" + b.token + "/" + method
}

func (b *botClient) do(req *http.Request, out any) error {
	resp, err := b.http.Do(req)
	if err != nil {
		return b.redact(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return b.redact(err)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram: unreadable response (HTTP %d)", resp.StatusCode)
	}
	if !env.OK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &apiError{Code: code, Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram: unexpected result shape: %w", err)
		}
	}
	return nil
}

func (b *botClient) callJSON(ctx context.Context, timeout time.Duration, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint(method), bytes.NewReader(body))
	if err != nil {
		return b.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return b.do(req, out)
}

// callJSONRetry retries once when Telegram asks us to slow down, which keeps a
// burst of button presses from surfacing as errors.
func (b *botClient) callJSONRetry(ctx context.Context, method string, payload, out any) error {
	err := b.callJSON(ctx, callTimeout, method, payload, out)
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == http.StatusTooManyRequests && ae.RetryAfter > 0 && ae.RetryAfter <= 5 {
		select {
		case <-time.After(time.Duration(ae.RetryAfter) * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		return b.callJSON(ctx, callTimeout, method, payload, out)
	}
	return err
}

func (b *botClient) getUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tgUpdate, error) {
	var out []tgUpdate
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	}
	err := b.callJSON(ctx, time.Duration(timeoutSec)*time.Second+15*time.Second, "getUpdates", payload, &out)
	return out, err
}

type sendMessageBody struct {
	ChatID      int64          `json:"chat_id"`
	Text        string         `json:"text"`
	ParseMode   string         `json:"parse_mode"`
	ReplyMarkup *replyMarkup   `json:"reply_markup,omitempty"`
	LinkPreview map[string]any `json:"link_preview_options"`
}

type replyMarkup struct {
	InlineKeyboard keyboard `json:"inline_keyboard"`
}

func markupFor(kb keyboard) *replyMarkup {
	if len(kb) == 0 {
		return nil
	}
	return &replyMarkup{InlineKeyboard: kb}
}

var noPreview = map[string]any{"is_disabled": true}

func (b *botClient) sendMessage(ctx context.Context, chatID int64, text string, kb keyboard) (int, error) {
	var out tgMessage
	err := b.callJSONRetry(ctx, "sendMessage", sendMessageBody{
		ChatID: chatID, Text: text, ParseMode: "HTML", ReplyMarkup: markupFor(kb), LinkPreview: noPreview,
	}, &out)
	return out.MessageID, err
}

type editMessageBody struct {
	ChatID      int64          `json:"chat_id"`
	MessageID   int            `json:"message_id"`
	Text        string         `json:"text"`
	ParseMode   string         `json:"parse_mode"`
	ReplyMarkup *replyMarkup   `json:"reply_markup,omitempty"`
	LinkPreview map[string]any `json:"link_preview_options"`
}

func (b *botClient) editMessage(ctx context.Context, chatID int64, msgID int, text string, kb keyboard) error {
	return b.callJSONRetry(ctx, "editMessageText", editMessageBody{
		ChatID: chatID, MessageID: msgID, Text: text, ParseMode: "HTML", ReplyMarkup: markupFor(kb), LinkPreview: noPreview,
	}, nil)
}

func (b *botClient) answerCallback(ctx context.Context, id, text string) error {
	payload := map[string]any{"callback_query_id": id}
	if text != "" {
		payload["text"] = text
	}
	return b.callJSON(ctx, callTimeout, "answerCallbackQuery", payload, nil)
}

func (b *botClient) deleteMessage(ctx context.Context, chatID int64, msgID int) error {
	return b.callJSON(ctx, callTimeout, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": msgID}, nil)
}

// sendFile uploads one file with sendDocument/sendPhoto. Uploads can be tens of
// megabytes, so the deadline is longer than for a plain call.
func (b *botClient) sendFile(ctx context.Context, method, field string, chatID int64, filename, contentType string, data []byte, caption string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", fmt.Sprint(chatID))
	if caption != "" {
		_ = w.WriteField("caption", caption)
		_ = w.WriteField("parse_mode", "HTML")
	}
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	hdr.Set("Content-Type", contentType)
	part, err := w.CreatePart(hdr)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint(method), &buf)
	if err != nil {
		return b.redact(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return b.do(req, nil)
}

func (b *botClient) sendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string) error {
	return b.sendFile(ctx, "sendDocument", "document", chatID, filename, "application/gzip", data, caption)
}

func (b *botClient) sendPhoto(ctx context.Context, chatID int64, png []byte, caption string) error {
	return b.sendFile(ctx, "sendPhoto", "photo", chatID, "qr.png", "image/png", png, caption)
}
